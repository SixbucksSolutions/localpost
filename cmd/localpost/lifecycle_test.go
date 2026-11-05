package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type lifecycleReply struct {
	status int
	body   []byte
	err    error
}

func lifecycleRequest(client *http.Client, method, url string) lifecycleReply {
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		return lifecycleReply{err: err}
	}
	response, err := client.Do(request)
	if err != nil {
		return lifecycleReply{err: err}
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	return lifecycleReply{status: response.StatusCode, body: body, err: err}
}

func lifecycleServer(t *testing.T, grace time.Duration) (*registry, string, context.CancelFunc, <-chan error) {
	t.Helper()
	r := newRegistry(t.TempDir())
	r.gracePeriod = grace
	listener := newTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveWithRegistry(ctx, listener, r) }()
	t.Cleanup(func() { cancel(); r.close() })
	return r, "http://" + listener.Addr().String(), cancel, done
}

func lifecycleClient(t *testing.T) *http.Client {
	t.Helper()
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func lifecycleRegister(t *testing.T, client *http.Client, url string) registration {
	t.Helper()
	body, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(url+apiPrefix+"/registrations", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result registration
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.ID == "" {
		t.Fatalf("registration = %d %+v", response.StatusCode, result)
	}
	return result
}

func lifecycleMailbox(r *registry, id string) *registration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registrations[id]
}

func lifecycleWait(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-deadline.C:
			t.Fatal("lifecycle state did not arrive before deadline")
		case <-ticker.C:
		}
	}
}

func lifecycleUpload(t *testing.T, url string, mailbox registration, directory string) (net.Conn, *bufio.Reader) {
	t.Helper()
	return lifecycleUploadLength(t, url, mailbox, directory, 6)
}

func lifecycleUploadLength(t *testing.T, url string, mailbox registration, directory string, length int) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\nabc", messagePath(mailbox.ID), length); err != nil {
		t.Fatal(err)
	}
	assertDirectoryEntries(t, directory, 1)
	return conn, bufio.NewReader(conn)
}

func lifecycleCheckDelete(t *testing.T, reply lifecycleReply, id string, forced bool) {
	t.Helper()
	var result struct {
		ID           string `json:"id"`
		Unregistered bool   `json:"unregistered"`
		Forced       bool   `json:"forced"`
	}
	if reply.err != nil || reply.status != http.StatusOK {
		t.Fatalf("DELETE = %d %s %v", reply.status, reply.body, reply.err)
	}
	if err := json.Unmarshal(reply.body, &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != id || !result.Unregistered || result.Forced != forced {
		t.Fatalf("DELETE result = %+v", result)
	}
}

func lifecycleAssertReleased(t *testing.T, r *registry, mailbox registration) {
	t.Helper()
	if lifecycleMailbox(r, mailbox.ID) != nil {
		t.Fatal("finished registration retained mapping")
	}
}

func messagePath(id string) string { return apiPrefix + "/registrations/" + id + "/messages" }

func TestLifecycleUnregisterPreservesFilesAndSibling(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, time.Second)
	client := lifecycleClient(t)
	first := lifecycleRegister(t, client, url)
	directory := first.Directory
	sibling := lifecycleRegister(t, client, url)
	path := filepath.Join(directory, "existing.dat")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	lifecycleCheckDelete(t, lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+first.ID), first.ID, false)
	lifecycleAssertReleased(t, r, first)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "retained" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
	response, err := client.Post(url+messagePath(sibling.ID), "application/octet-stream", strings.NewReader("still usable"))
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sibling response = %d", response.StatusCode)
	}
	for _, tc := range []struct {
		method, id string
		status     int
		code       string
	}{
		{http.MethodDelete, first.ID, 404, "registration_not_found"},
		{http.MethodDelete, "12345678-1234-4234-8234-123456789abc", 404, "registration_not_found"},
		{http.MethodGet, sibling.ID, 405, "method_not_allowed"},
		{http.MethodPost, sibling.ID, 405, "method_not_allowed"},
	} {
		reply := lifecycleRequest(client, tc.method, url+apiPrefix+"/registrations/"+tc.id)
		if reply.err != nil || reply.status != tc.status || !bytes.Contains(reply.body, []byte(tc.code)) {
			t.Fatalf("route result = %+v", reply)
		}
	}
}

func TestLifecycleAcceptedUploadDrainsAndRejectsNewWork(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, 2*time.Second)
	client := lifecycleClient(t)
	registered := lifecycleRegister(t, client, url)
	directory := registered.Directory
	mailbox := lifecycleMailbox(r, registered.ID)
	conn, reader := lifecycleUpload(t, url, registered, directory)
	// A partial header keeps an accepted connection alive across the closing gate.
	waiting, err := net.DialTimeout("tcp4", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiting.Close() }()
	if err := waiting.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(waiting, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	waitingReader := bufio.NewReader(waiting)
	prior, err := http.ReadResponse(waitingReader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, prior.Body); err != nil {
		t.Fatal(err)
	}
	if err := prior.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(waiting, "POST "+messagePath(registered.ID)+" HTTP/1.1\r\n"); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan lifecycleReply, 1)
	go func() {
		deleted <- lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+registered.ID)
	}()
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	rejected := lifecycleRequest(client, http.MethodPost, url+messagePath(registered.ID))
	if rejected.err != nil || rejected.status != http.StatusServiceUnavailable || !bytes.Contains(rejected.body, []byte(`"code":"unavailable"`)) {
		t.Fatalf("closing route = %+v", rejected)
	}
	select {
	case result := <-deleted:
		t.Fatalf("DELETE returned before admitted body completed: %+v", result)
	default:
	}
	if _, err := io.WriteString(waiting, "Host: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 0\r\n\r\n"); err == nil {
		response, readErr := http.ReadResponse(waitingReader, nil)
		if readErr == nil {
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("new request accepted during close: %d", response.StatusCode)
			}
		}
	}
	if _, err := io.WriteString(conn, "def"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	var receipt messageReceipt
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admitted upload = %d", response.StatusCode)
	}
	lifecycleCheckDelete(t, <-deleted, registered.ID, false)
	data, err := os.ReadFile(filepath.Join(directory, receipt.Filename))
	if err != nil || string(data) != "abcdef" {
		t.Fatalf("drained file = %q %v", data, err)
	}
	lifecycleAssertReleased(t, r, registered)
}

func TestLifecycleForcedDeadlineDoesNotExtendWithProgress(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, 350*time.Millisecond)
	client := lifecycleClient(t)
	registered := lifecycleRegister(t, client, url)
	directory := registered.Directory
	mailbox := lifecycleMailbox(r, registered.ID)
	conn, _ := lifecycleUploadLength(t, url, registered, directory, 65536)
	deleted := make(chan lifecycleReply, 1)
	start := time.Now()
	go func() {
		deleted <- lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+registered.ID)
	}()
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	// Continuous progress stays below the idle timeout, but cannot extend grace.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	progress := 0
waitForForcedStop:
	for {
		select {
		case reply := <-deleted:
			lifecycleCheckDelete(t, reply, registered.ID, true)
			break waitForForcedStop
		case <-ticker.C:
			// A close racing the write is expected at the forced deadline.
			if _, err := io.WriteString(conn, "x"); err == nil {
				progress++
			}
		case <-deadline.C:
			t.Fatal("continuous body progress extended the shutdown budget")
		}
	}
	if progress == 0 || time.Since(start) > time.Second {
		t.Fatal("forced deadline was not exercised with progress")
	}
	assertDirectoryEntries(t, directory, 0)
	// Completing the body after the reported cutoff cannot publish a late file.
	_, _ = io.WriteString(conn, "z")
	assertDirectoryEntries(t, directory, 0)
	lifecycleAssertReleased(t, r, registered)
}

func TestLifecycleConcurrentDeleteCoalesces(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, 600*time.Millisecond)
	client := lifecycleClient(t)
	registered := lifecycleRegister(t, client, url)
	directory := registered.Directory
	mailbox := lifecycleMailbox(r, registered.ID)
	_, _ = lifecycleUpload(t, url, registered, directory)
	replies := make(chan lifecycleReply, 8)
	for range cap(replies) {
		go func() {
			replies <- lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+registered.ID)
		}()
	}
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	for range cap(replies) {
		lifecycleCheckDelete(t, <-replies, registered.ID, true)
	}
	assertDirectoryEntries(t, directory, 0)
	lifecycleAssertReleased(t, r, registered)
}

func TestLifecycleProcessCancellationDrainsAcceptedUpload(t *testing.T) {
	r, url, cancel, done := lifecycleServer(t, 2*time.Second)
	client := lifecycleClient(t)
	registered := lifecycleRegister(t, client, url)
	directory := registered.Directory
	mailbox := lifecycleMailbox(r, registered.ID)
	conn, reader := lifecycleUpload(t, url, registered, directory)
	cancel()
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	if _, err := io.WriteString(conn, "def"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("process shutdown discarded admitted upload: %d", response.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("process shutdown did not finish")
	}
	assertDirectoryEntries(t, directory, 1)
	lifecycleAssertReleased(t, r, registered)
}

func TestLifecycleProcessCancellationUsesSharedBudget(t *testing.T) {
	r, url, cancel, done := lifecycleServer(t, 300*time.Millisecond)
	client := lifecycleClient(t)
	var registered []registration
	var directories []string
	for range 5 {
		mailbox := lifecycleRegister(t, client, url)
		directory := mailbox.Directory
		_, _ = lifecycleUpload(t, url, mailbox, directory)
		registered = append(registered, mailbox)
		directories = append(directories, directory)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mailbox shutdown budgets accumulated serially")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shared shutdown budget exceeded: %v", elapsed)
	}
	for i, mailbox := range registered {
		lifecycleAssertReleased(t, r, mailbox)
		assertDirectoryEntries(t, directories[i], 0)
	}
	response, _ := registrationRequest(t, r)
	if response.Code != http.StatusServiceUnavailable || registrationCount(r) != 0 {
		t.Fatal("global shutdown admitted a later registration")
	}
}

func TestLifecycleDeleteContinuesAfterCallerDisconnect(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, 2*time.Second)
	client := lifecycleClient(t)
	registered := lifecycleRegister(t, client, url)
	directory := registered.Directory
	mailbox := lifecycleMailbox(r, registered.ID)
	conn, reader := lifecycleUpload(t, url, registered, directory)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, url+apiPrefix+"/registrations/"+registered.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	callerDone := make(chan error, 1)
	go func() {
		response, callErr := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		callerDone <- callErr
	}()
	lifecycleWait(t, func() bool { mailbox.mu.Lock(); defer mailbox.mu.Unlock(); return mailbox.closing })
	cancel()
	select {
	case err := <-callerDone:
		if err == nil {
			t.Fatal("DELETE caller completed before cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("DELETE caller did not disconnect")
	}
	if _, err := io.WriteString(conn, "def"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disconnect interrupted admitted upload: %d", response.StatusCode)
	}
	select {
	case <-mailbox.done:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected DELETE did not finish unregistering")
	}
	if lifecycleMailbox(r, registered.ID) != nil {
		t.Fatal("disconnected DELETE retained registration")
	}
	assertDirectoryEntries(t, directory, 1)
	lifecycleAssertReleased(t, r, registered)
}

func TestLifecycleForcedDeleteDoesNotAbortReusedSiblingConnection(t *testing.T) {
	r, url, _, _ := lifecycleServer(t, 100*time.Millisecond)
	client := lifecycleClient(t)
	first := lifecycleRegister(t, client, url)
	firstDirectory := first.Directory
	sibling := lifecycleRegister(t, client, url)
	siblingDirectory := sibling.Directory
	firstOwner, siblingOwner := lifecycleMailbox(r, first.ID), lifecycleMailbox(r, sibling.ID)
	shared, err := net.DialTimeout("tcp4", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	if err := shared.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(shared)
	if _, err := fmt.Fprintf(shared, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 1\r\n\r\nx", messagePath(first.ID)); err != nil {
		t.Fatal(err)
	}
	resourceReadResponse(t, reader, http.StatusOK)
	lifecycleWait(t, func() bool { firstOwner.mu.Lock(); defer firstOwner.mu.Unlock(); return firstOwner.active == 0 })
	// Reuse the completed A connection for an admitted but unfinished B upload.
	if _, err := fmt.Fprintf(shared, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 6\r\n\r\nabc", messagePath(sibling.ID)); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, func() bool { siblingOwner.mu.Lock(); defer siblingOwner.mu.Unlock(); return siblingOwner.active == 1 })
	held, err := net.DialTimeout("tcp4", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := held.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(held, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 6\r\n\r\nabc", messagePath(first.ID)); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, func() bool { firstOwner.mu.Lock(); defer firstOwner.mu.Unlock(); return firstOwner.active == 1 })
	// Forced A cleanup must target its current request, never its former TCP
	// connection now owned by B. The DELETE response proves force completed.
	lifecycleCheckDelete(t, lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+first.ID), first.ID, true)
	if _, err := io.WriteString(shared, "def"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	var receipt messageReceipt
	decodeErr := json.NewDecoder(response.Body).Decode(&receipt)
	closeErr := response.Body.Close()
	if decodeErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || response.Close || receipt.Bytes != 6 {
		t.Fatalf("sibling completion = %d %+v %v %v", response.StatusCode, receipt, decodeErr, closeErr)
	}
	payload, err := os.ReadFile(filepath.Join(siblingDirectory, receipt.Filename))
	if err != nil || string(payload) != "abcdef" {
		t.Fatalf("sibling payload = %q %v", payload, err)
	}
	assertDirectoryEntries(t, firstDirectory, 1)
	assertDirectoryEntries(t, siblingDirectory, 1)
	lifecycleWait(t, func() bool { return len(r.uploads.held) == 0 })
	if lifecycleMailbox(r, first.ID) != nil || lifecycleMailbox(r, sibling.ID) != siblingOwner {
		t.Fatal("forced deletion changed sibling ownership")
	}
}
