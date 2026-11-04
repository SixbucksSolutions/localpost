package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResourceRegistrationConcurrentBoundary(t *testing.T) {
	r := newRegistry()
	r.registrationLimit = 4
	defer r.close()
	results := make(chan error, 32)
	start := make(chan struct{})
	directory := t.TempDir()
	for range cap(results) {
		go func() { <-start; _, err := r.register(directory); results <- err }()
	}
	close(start)
	accepted := 0
	for range cap(results) {
		if err := <-results; err == nil {
			accepted++
		} else if !errors.Is(err, errRegistrationLimit) {
			t.Fatal(err)
		}
	}
	if accepted != 4 || registrationCount(r) != 4 {
		t.Fatalf("accepted %d registrations", accepted)
	}
	response, _ := registrationRequest(t, withCORS(r, "POST, DELETE, OPTIONS"), directory)
	if response.Code != 503 || !strings.Contains(response.Body.String(), `"code":"registration_limit"`) || response.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("saturation response: %d %s", response.Code, response.Body.String())
	}
}

func TestResourceRegistrationFailedAllocationReusesCapacity(t *testing.T) {
	r := newRegistry()
	r.registrationLimit = 1
	defer r.close()
	r.listen = func(string, string) (net.Listener, error) { return nil, errors.New("test allocation failure") }
	if _, err := r.register(t.TempDir()); err == nil {
		t.Fatal("allocation unexpectedly succeeded")
	}
	r.listen = net.Listen
	if _, err := r.register(t.TempDir()); err != nil {
		t.Fatalf("failed allocation consumed capacity: %v", err)
	}
}

func TestResourceRegistrationClosingRetainsCapacity(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, 2*time.Second)
	r.registrationLimit = 1
	client := lifecycleClient(t)
	directory := t.TempDir()
	registered := lifecycleRegister(t, client, url, directory)
	mailbox := lifecycleMailbox(r, registered.ID)
	conn, reader := lifecycleUpload(t, registered, directory)
	deleted := make(chan lifecycleReply, 1)
	go func() { deleted <- lifecycleRequest(client, http.MethodDelete, url+"/registrations/"+registered.ID) }()
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	if _, err := r.register(directory); !errors.Is(err, errRegistrationLimit) {
		t.Fatalf("closing registration released capacity: %v", err)
	}
	if _, err := io.WriteString(conn, "def"); err != nil {
		t.Fatal(err)
	}
	resourceReadResponse(t, reader, http.StatusOK)
	lifecycleCheckDelete(t, <-deleted, registered.ID, false)
	if _, err := r.register(directory); err != nil {
		t.Fatalf("finalized registration retained capacity: %v", err)
	}
}

func resourceReadResponse(t *testing.T, reader *bufio.Reader, status int) {
	t.Helper()
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("response = %d, want %d", response.StatusCode, status)
	}
}

func resourcePost(t *testing.T, client *http.Client, port int, status int) {
	t.Helper()
	response, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/messages", port), "application/json", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("POST = %d %s, want %d", response.StatusCode, data, status)
	}
	if status == 503 && (!strings.Contains(string(data), `"code":"upload_limit"`) || response.Header.Get("Access-Control-Allow-Origin") != "*") {
		t.Fatalf("invalid saturation response: %s", data)
	}
}

func TestResourceUploadGlobalSaturationAndCancellation(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, time.Second)
	r.uploads = newUploadSlots(1)
	client := lifecycleClient(t)
	directory := t.TempDir()
	first := lifecycleRegister(t, client, url, directory)
	siblingDir := t.TempDir()
	sibling := lifecycleRegister(t, client, url, siblingDir)
	conn, _ := lifecycleUpload(t, first, directory)
	// An incomplete rejected body must receive a response without sending its tail.
	rejected, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", sibling.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rejected.Close() }()
	if err := rejected.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(rejected, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(rejected), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 503 || !strings.Contains(string(data), `"code":"upload_limit"`) {
		t.Fatalf("saturation = %d %s %v", response.StatusCode, data, err)
	}
	assertDirectoryEntries(t, siblingDir, 0)
	resourcePost(t, client, sibling.Port, 503)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, func() bool { return len(r.uploads.held) == 0 })
	assertDirectoryEntries(t, directory, 0)
	resourcePost(t, client, sibling.Port, 200)
	resourcePost(t, client, first.Port, 200)
	lifecycleWait(t, func() bool { return len(r.uploads.held) == 0 })
}

func TestResourceUploadReleaseOnFailures(t *testing.T) {
	for _, failure := range []string{"create", "publish", "oversize"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			server, handler := newDeliveryServer(t, directory, 0)
			handler.uploads = newUploadSlots(1)
			switch failure {
			case "create":
				handler.directory = filepath.Join(directory, "missing")
			case "publish":
				handler.filename = func() string { return "existing.dat" }
				if err := os.WriteFile(filepath.Join(directory, "existing.dat"), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var body io.Reader = strings.NewReader("payload")
			expected := 500
			if failure == "oversize" {
				body = io.LimitReader(zeroReader{}, messageBodyLimit+1)
				expected = 413
			}
			request, err := http.NewRequest(http.MethodPost, server.URL+"/messages", body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != expected {
				t.Fatalf("failure response = %d", response.StatusCode)
			}
			lifecycleWait(t, func() bool { return len(handler.uploads.held) == 0 })
			handler.directory = directory
			handler.filename = func() string { return "recovered.dat" }
			status, _, result := deliveryRequest(t, server, "application/json", []byte("recovered"))
			if status != 200 {
				t.Fatalf("capacity was not reusable: %d %s", status, result)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestResourceUploadForcedDrainAndShutdownRelease(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			r, url, cancel, done := lifecycleServer(t, 100*time.Millisecond)
			r.uploads = newUploadSlots(1)
			client := lifecycleClient(t)
			directory := t.TempDir()
			first := lifecycleRegister(t, client, url, directory)
			sibling := lifecycleRegister(t, client, url, t.TempDir())
			_, _ = lifecycleUpload(t, first, directory)
			if shutdown {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("shutdown stalled")
				}
				if registrationCount(r) != 0 {
					t.Fatal("shutdown retained registrations")
				}
			} else {
				lifecycleCheckDelete(t, lifecycleRequest(client, http.MethodDelete, url+"/registrations/"+first.ID), first.ID, true)
				resourcePost(t, client, sibling.Port, 200)
			}
			lifecycleWait(t, func() bool { return len(r.uploads.held) == 0 })
			assertDirectoryEntries(t, directory, 0)
		})
	}
}

func TestResourceUploadConcurrentBoundary(t *testing.T) {
	slots := newUploadSlots(4)
	var workers sync.WaitGroup
	results := make(chan bool, 32)
	release := make(chan struct{})
	for range cap(results) {
		workers.Go(func() {
			accepted := slots.acquire()
			results <- accepted
			<-release
			if accepted {
				slots.release()
			}
		})
	}
	accepted := 0
	for range cap(results) {
		if <-results {
			accepted++
		}
	}
	if accepted != 4 {
		t.Fatalf("accepted %d uploads", accepted)
	}
	close(release)
	workers.Wait()
	if len(slots.held) != 0 {
		t.Fatal("concurrent uploads leaked capacity")
	}
}

func TestResourceUploadGracefulShutdownRelease(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, time.Second)
	r.uploads = newUploadSlots(1)
	client := lifecycleClient(t)
	directory := t.TempDir()
	first := lifecycleRegister(t, client, url, directory)
	conn, reader := lifecycleUpload(t, first, directory)
	done := make(chan struct{})
	go func() { r.shutdown(context.Background()); close(done) }()
	mailbox := lifecycleMailbox(r, first.ID)
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	if len(r.uploads.held) != 1 {
		t.Fatal("draining upload released capacity early")
	}
	if _, err := io.WriteString(conn, "def"); err != nil {
		t.Fatal(err)
	}
	resourceReadResponse(t, reader, 200)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown stalled")
	}
	if len(r.uploads.held) != 0 || registrationCount(r) != 0 {
		t.Fatal("shutdown leaked capacity")
	}
	assertDirectoryEntries(t, directory, 1)
}

func TestResourceUnexpectedListenerFailureReleasesCapacity(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, time.Second)
	r.registrationLimit = 1
	r.uploads = newUploadSlots(1)
	client := lifecycleClient(t)
	directory := t.TempDir()
	registered := lifecycleRegister(t, client, url, directory)
	mailbox := lifecycleMailbox(r, registered.ID)
	_, _ = lifecycleUpload(t, registered, directory)
	if err := mailbox.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mailbox.done:
	case <-time.After(2 * time.Second):
		t.Fatal("failed listener did not finish cleanup")
	}
	if len(r.uploads.held) != 0 || registrationCount(r) != 0 {
		t.Fatal("listener failure retained capacity")
	}
	assertDirectoryEntries(t, directory, 0)
	replacement := lifecycleRegister(t, client, url, t.TempDir())
	resourcePost(t, client, replacement.Port, 200)
}

// delayedCloseListener keeps Serve's listener tracked after Close. A premature
// external Close would make Shutdown observe the second Close's net.ErrClosed.
type delayedCloseListener struct {
	net.Listener
	accepting     chan struct{}
	firstClose    chan struct{}
	secondClose   chan struct{}
	releaseAccept chan struct{}
	closeMu       sync.Mutex
	closeCount    int
}

func (l *delayedCloseListener) Accept() (net.Conn, error) {
	close(l.accepting)
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.releaseAccept
	}
	return conn, err
}

func (l *delayedCloseListener) Close() error {
	l.closeMu.Lock()
	defer l.closeMu.Unlock()
	l.closeCount++
	if l.closeCount == 1 {
		err := l.Listener.Close()
		close(l.firstClose)
		return err
	}
	if l.closeCount == 2 {
		close(l.secondClose)
	}
	return net.ErrClosed
}

func TestResourceGracefulDrainOwnsListenerClose(t *testing.T) {
	listener := &delayedCloseListener{
		Listener: newTestListener(t), accepting: make(chan struct{}),
		firstClose: make(chan struct{}), secondClose: make(chan struct{}), releaseAccept: make(chan struct{}),
	}
	r := newRegistry()
	defer r.close()
	r.listen = func(string, string) (net.Listener, error) { return listener, nil }
	mailbox, err := r.register(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	<-listener.accepting
	result := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { r.unregister(result, mailbox.ID); close(done) }()
	<-listener.firstClose
	// With the old close-before-Shutdown sequence, the second Close arrives
	// while Accept remains held here, deterministically retaining the error.
	select {
	case <-listener.secondClose:
	case <-time.After(30 * time.Millisecond):
	}
	close(listener.releaseAccept)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not finish")
	}
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"forced":false`) {
		t.Fatalf("graceful listener close reported forced: %d %s", result.Code, result.Body.String())
	}
	if registrationCount(r) != 0 {
		t.Fatal("graceful drain retained registration capacity")
	}
}

func TestResourceDrainBeforeServeReleasesListener(t *testing.T) {
	r := newRegistry()
	defer r.close()
	listener := newTestListener(t)
	mailbox := &registration{
		ID: "not-started", listener: listener, server: &http.Server{},
		mu: &sync.Mutex{}, drained: make(chan struct{}), done: make(chan struct{}),
	}
	r.mu.Lock()
	r.registrations[mailbox.ID] = mailbox
	r.startStop(mailbox)
	r.mu.Unlock()
	select {
	case <-mailbox.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pre-Serve drain did not finish")
	}
	if mailbox.forced || registrationCount(r) != 0 {
		t.Fatal("pre-Serve drain was forced or retained capacity")
	}
	if err := mailbox.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("late Serve = %v", err)
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("pre-Serve listener not released: %v", err)
	}
}
