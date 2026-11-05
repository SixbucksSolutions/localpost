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

// Exercise the caps, fault recovery and lifecycle together across fresh daemon
// instances. All retained payloads live in temporary directories; every owned
// registration, handler and listening port must be released before restart.
func TestQualificationCombinedCycles(t *testing.T) {
	client := lifecycleClient(t)
	address := "127.0.0.1:0"
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	retained := []map[string][]byte{{}, {}, {}}
	for cycle := range 3 {
		r, url, cancel, done := qualificationServer(t, address)
		address = url[len("http://"):]
		registered := make([]registration, 3)
		owned := make([]*registration, 0, 4)
		for i := range registered {
			registered[i] = lifecycleRegister(t, client, url)
			directories[i] = registered[i].Directory
			retained[i] = map[string][]byte{}
			owned = append(owned, lifecycleMailbox(r, registered[i].ID))
			payload := []byte{0, 255, byte(cycle), byte(i), '\r', '\n'}
			name := qualificationPost(t, client, url, registered[i], payload, http.StatusOK)
			retained[i][name] = payload
		}
		response, _ := registrationRequest(t, r)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("cycle %d: registration cap = %d", cycle, response.Code)
		}

		first, reader := qualificationUpload(t, url, registered[0])
		canceled, _ := qualificationUpload(t, url, registered[1])
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 2 })
		qualificationPost(t, client, url, registered[2], []byte("saturated"), http.StatusServiceUnavailable)
		if err := canceled.Close(); err != nil {
			t.Fatal(err)
		}
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 1 })

		// Rename the registered directory to inject a real create failure without
		// changing process-wide permissions or replacing the production handler.
		moved := directories[2] + "-offline"
		if err := os.Rename(directories[2], moved); err != nil {
			t.Fatal(err)
		}
		qualificationPost(t, client, url, registered[2], []byte("unavailable storage"), http.StatusInternalServerError)
		if err := os.Rename(moved, directories[2]); err != nil {
			t.Fatal(err)
		}
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 1 })
		payload := []byte(fmt.Sprintf("recovered sibling %d\x00\xff", cycle))
		name := qualificationPost(t, client, url, registered[2], payload, http.StatusOK)
		retained[2][name] = payload

		replies := make(chan lifecycleReply, 2)
		start := make(chan struct{})
		for range cap(replies) {
			go func() {
				<-start
				replies <- lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+registered[0].ID)
			}()
		}
		close(start)
		lifecycleWait(t, func() bool { owned[0].mu.Lock(); defer owned[0].mu.Unlock(); return owned[0].closing })
		response, _ = registrationRequest(t, r)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatal("draining mailbox released registration capacity early")
		}
		forced := cycle%2 == 1
		if !forced {
			if _, err := io.WriteString(first, "def"); err != nil {
				t.Fatal(err)
			}
			receipt := qualificationReceipt(t, reader, http.StatusOK)
			retained[0][receipt.Filename] = []byte("abcdef")
		}
		completed := 0
		for range cap(replies) {
			reply := <-replies
			// A duplicate that reaches the registry after finalization correctly sees
			// 404; every DELETE that joined the live mapping reports the same outcome.
			if reply.err == nil && reply.status == http.StatusNotFound {
				continue
			}
			lifecycleCheckDelete(t, reply, registered[0].ID, forced)
			completed++
		}
		if completed == 0 {
			t.Fatal("no DELETE observed the owned drain")
		}
		if forced {
			_, _ = io.WriteString(first, "def")
			if late, err := http.ReadResponse(reader, nil); err == nil {
				_ = late.Body.Close()
				if late.StatusCode == http.StatusOK {
					t.Fatal("forced upload reported publication")
				}
			}
		}
		qualificationReleased(t, r, owned[0])

		qualificationFiles(t, directories[0], retained[0])
		replacement := lifecycleRegister(t, client, url)
		directories[0] = replacement.Directory
		retained[0] = map[string][]byte{}
		replacementOwner := lifecycleMailbox(r, replacement.ID)
		owned = append(owned, replacementOwner)
		pending, pendingReader := qualificationUpload(t, url, replacement)
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 1 })
		cancel()
		lifecycleWait(t, func() bool {
			replacementOwner.mu.Lock()
			defer replacementOwner.mu.Unlock()
			return replacementOwner.closing
		})
		if !forced {
			if _, err := io.WriteString(pending, "def"); err != nil {
				t.Fatal(err)
			}
			receipt := qualificationReceipt(t, pendingReader, http.StatusOK)
			retained[0][receipt.Filename] = []byte("abcdef")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("daemon shutdown retained work")
		}
		if forced {
			_, _ = io.WriteString(pending, "def")
		}
		if registrationCount(r) != 0 || len(r.uploads.held) != 0 {
			t.Fatal("shutdown retained owned capacity")
		}
		for _, mailbox := range owned {
			qualificationReleased(t, r, mailbox)
		}
		for i, directory := range directories {
			qualificationFiles(t, directory, retained[i])
		}
		client.CloseIdleConnections()
		// The next cycle binds this same admin address and allocates new
		// mailboxes, proving restart without in-memory ownership.
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("admin port retained after final shutdown: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func qualificationServer(t *testing.T, address string) (*registry, string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	r := newRegistry(t.TempDir())
	r.registrationLimit = 3
	r.uploads = newUploadSlots(2)
	r.gracePeriod = 500 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveWithRegistry(ctx, listener, r) }()
	t.Cleanup(func() { cancel(); r.close() })
	return r, "http://" + listener.Addr().String(), cancel, done
}

func qualificationUpload(t *testing.T, url string, mailbox registration) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "POST "+messagePath(mailbox.ID)+" HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 6\r\n\r\nabc"); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

func qualificationReceipt(t *testing.T, reader *bufio.Reader, status int) messageReceipt {
	t.Helper()
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var receipt messageReceipt
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || receipt.Bytes != 6 || receipt.Filename == "" {
		t.Fatalf("upload receipt: status %d, %+v", response.StatusCode, receipt)
	}
	return receipt
}

func qualificationPost(t *testing.T, client *http.Client, url string, mailbox registration, payload []byte, status int) string {
	t.Helper()
	response, err := client.Post(url+messagePath(mailbox.ID), "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("upload = %d %s, want %d", response.StatusCode, body, status)
	}
	if status != http.StatusOK {
		return ""
	}
	var receipt messageReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Bytes != int64(len(payload)) || receipt.Filename == "" {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	return receipt.Filename
}

func qualificationReleased(t *testing.T, r *registry, mailbox *registration) {
	t.Helper()
	select {
	case <-mailbox.done:
	default:
		t.Fatal("mailbox cleanup is unfinished")
	}
	mailbox.mu.Lock()
	active := mailbox.active
	mailbox.mu.Unlock()
	if active != 0 || lifecycleMailbox(r, mailbox.ID) != nil {
		t.Fatal("finished mailbox retained handlers or registry ownership")
	}
	lifecycleAssertReleased(t, r, *mailbox)
}

func qualificationFiles(t *testing.T, directory string, expected map[string][]byte) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(expected) {
		t.Fatalf("directory retained unexpected files: got %d, want %d", len(entries), len(expected))
	}
	for _, entry := range entries {
		payload, ok := expected[entry.Name()]
		if !ok {
			t.Fatalf("unexpected file or temporary remnant: %s", entry.Name())
		}
		actual, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil || !bytes.Equal(actual, payload) {
			t.Fatalf("retained payload changed: %s (%v)", entry.Name(), err)
		}
	}
}
