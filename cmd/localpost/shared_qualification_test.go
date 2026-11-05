package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// DELETE and process shutdown must share ownership of a draining registration,
// while sibling uploads independently finish or reach the forced cutoff.
func TestQualificationSharedListenerOverlappingShutdown(t *testing.T) {
	client := lifecycleClient(t)
	address := "127.0.0.1:0"
	directories := [3]string{t.TempDir(), t.TempDir(), t.TempDir()}
	retained := [3]map[string][]byte{{}, {}, {}}
	var previous [3]registration
	for cycle := range 3 {
		r, url, cancel, done := qualificationServer(t, address)
		address = strings.TrimPrefix(url, "http://")
		for i, old := range previous {
			if old.ID != "" {
				qualificationPost(t, client, url, old, []byte("stale UUID after restart"), http.StatusNotFound)
				qualificationFiles(t, directories[i], retained[i])
			}
		}
		var registered [3]registration
		var owners [3]*registration
		var connections [3]net.Conn
		var readers [3]*bufio.Reader
		var payloads [3][]byte
		for i := range registered {
			registered[i] = lifecycleRegister(t, client, url)
			directories[i] = registered[i].Directory
			retained[i] = map[string][]byte{}
			if !validRegistrationID(registered[i].ID) {
				t.Fatalf("cycle %d: invalid registration UUID", cycle)
			}
			for j := range i {
				if registered[i].ID == registered[j].ID {
					t.Fatal("shared listener assigned duplicate registration UUIDs")
				}
			}
			owners[i] = lifecycleMailbox(r, registered[i].ID)
			payloads[i] = []byte{0, 255, byte(cycle), byte(i), '\r', '\n'}
			connections[i], readers[i] = sharedQualificationUpload(t, url, registered[i], payloads[i][:3])
			if i < 2 {
				lifecycleWait(t, func() bool { return len(r.uploads.held) == i+1 })
			}
		}
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 2 })
		// The production test fixture has two global slots. The third concurrent
		// request is rejected, then retried after a completed sibling releases one.
		resourceReadResponse(t, readers[2], http.StatusServiceUnavailable)
		sharedQualificationComplete(t, connections[1], readers[1], payloads[1], retained[1])
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 1 })
		connections[2], readers[2] = sharedQualificationUpload(t, url, registered[2], payloads[2][:3])
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 2 })
		// Re-admit B after C finishes, leaving A and B concurrently pending when
		// DELETE starts. C's acknowledged file must survive the later shutdown.
		sharedQualificationComplete(t, connections[2], readers[2], payloads[2], retained[2])
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 1 })
		payloads[1] = []byte{255, 0, byte(cycle), 'B', '\n', '\r'}
		connections[1], readers[1] = sharedQualificationUpload(t, url, registered[1], payloads[1][:3])
		lifecycleWait(t, func() bool { return len(r.uploads.held) == 2 })

		deleted := make(chan lifecycleReply, 1)
		go func() {
			deleted <- lifecycleRequest(client, http.MethodDelete, url+apiPrefix+"/registrations/"+registered[0].ID)
		}()
		lifecycleWait(t, func() bool { return sharedQualificationClosing(owners[0]) })
		if sharedQualificationClosing(owners[1]) || sharedQualificationClosing(owners[2]) {
			t.Fatal("DELETE closed a sibling registration")
		}
		select {
		case <-deleted:
			t.Fatal("DELETE returned before its admitted upload drained")
		default:
		}
		cancel()
		lifecycleWait(t, func() bool { return sharedQualificationClosing(owners[1]) })
		forceDelete := cycle%2 == 1
		if !forceDelete {
			sharedQualificationComplete(t, connections[0], readers[0], payloads[0], retained[0])
		}
		// B completes within process grace even when A remains stuck in DELETE.
		sharedQualificationComplete(t, connections[1], readers[1], payloads[1], retained[1])
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("overlapping DELETE and shutdown did not release the listener")
		}
		if forceDelete {
			// A late tail cannot publish after forced DELETE/global cleanup.
			_, _ = connections[0].Write(payloads[0][3:])
		}
		reply := <-deleted
		// At forced shutdown net/http may close the DELETE response connection;
		// the owned completion state below remains authoritative in that race.
		if reply.err == nil || !forceDelete {
			lifecycleCheckDelete(t, reply, registered[0].ID, forceDelete)
		}
		if registrationCount(r) != 0 || len(r.uploads.held) != 0 {
			t.Fatal("overlapping shutdown retained registration or upload capacity")
		}
		for i, owner := range owners {
			qualificationReleased(t, r, owner)
			owner.mu.Lock()
			forced, requests := owner.forced, len(owner.requests)
			owner.mu.Unlock()
			if forced != (i == 0 && forceDelete) || requests != 0 {
				t.Fatalf("cycle %d registration %d: forced=%t, retained requests=%d", cycle, i, forced, requests)
			}
			qualificationFiles(t, directories[i], retained[i])
		}
		previous = registered
		client.CloseIdleConnections()
	}
	// Rebinding each cycle and once more after the final cycle detects retained
	// listener ownership without relying on process-wide goroutine counts.
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func sharedQualificationUpload(t *testing.T, url string, mailbox registration, prefix []byte) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 6\r\n\r\n", messagePath(mailbox.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(prefix); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

func sharedQualificationComplete(t *testing.T, conn net.Conn, reader *bufio.Reader, payload []byte, retained map[string][]byte) {
	t.Helper()
	if _, err := conn.Write(payload[3:]); err != nil {
		t.Fatal(err)
	}
	receipt := qualificationReceipt(t, reader, http.StatusOK)
	name := receipt.Filename
	if len(name) != 50 || !strings.HasPrefix(name, "localpost_") || !strings.HasSuffix(name, ".dat") || name[24] != '7' || !strings.ContainsRune("89ab", rune(name[29])) {
		t.Fatalf("not a UUIDv7 filename: %q", name)
	}
	if _, exists := retained[name]; exists {
		t.Fatal("acknowledged filename was reused across cycles")
	}
	retained[name] = payload
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func sharedQualificationClosing(owner *registration) bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.closing
}
