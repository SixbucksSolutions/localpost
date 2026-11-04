package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newDeliveryServer(t *testing.T, directory string, idle time.Duration) (*httptest.Server, *messageHandler) {
	t.Helper()
	handler := newMessageHandler(directory)
	if idle != 0 {
		handler.idleTimeout = idle
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, handler
}

func deliveryRequest(t *testing.T, server *httptest.Server, mediaType string, body []byte) (int, messageReceipt, string) {
	t.Helper()
	response, err := server.Client().Post(server.URL+"/messages", mediaType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", response.Header.Get("Content-Type"))
	}
	var receipt messageReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("invalid response JSON: %v", err)
	}
	return response.StatusCode, receipt, string(data)
}

func assertDirectoryEntries(t *testing.T, directory string, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("directory has %d entries, want %d", len(entries), count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMessageOpaqueDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType string
		body            []byte
	}{
		{"binary", "application/octet-stream", []byte{0, 255, 13, 10, 0, 1}},
		{"invalid JSON", "application/json; charset=utf-8", []byte("{definitely not JSON\x00\xff")},
		{"empty", "application/json", nil},
		{"limit", "application/octet-stream; version=1", bytes.Repeat([]byte{0x91}, int(messageBodyLimit))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			server, _ := newDeliveryServer(t, directory, 0)
			status, receipt, body := deliveryRequest(t, server, tc.mediaType, tc.body)
			if status != http.StatusOK || receipt.Bytes != int64(len(tc.body)) {
				t.Fatalf("response = %d %s", status, body)
			}
			name := receipt.Filename
			if len(name) != 50 || !strings.HasPrefix(name, "localpost_") || !strings.HasSuffix(name, ".dat") || name[24] != '7' || !strings.ContainsRune("89ab", rune(name[29])) {
				t.Fatalf("not a UUIDv7 filename: %q", name)
			}
			actual, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil || !bytes.Equal(actual, tc.body) {
				t.Fatalf("file differs or unreadable: %v", err)
			}
			assertDirectoryEntries(t, directory, 1)
		})
	}
}

func TestMessageHTTPValidation(t *testing.T) {
	directory := t.TempDir()
	server, _ := newDeliveryServer(t, directory, 0)
	for _, mediaType := range []string{"", "text/plain", "application/json;broken"} {
		status, _, body := deliveryRequest(t, server, mediaType, []byte("test"))
		if status != http.StatusUnsupportedMediaType || !strings.Contains(body, "unsupported_media_type") {
			t.Fatalf("response = %d %s", status, body)
		}
	}
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/messages", 405, "method_not_allowed"},
		{http.MethodPost, "/unknown", 404, "not_found"},
	} {
		request, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != tc.status || !strings.Contains(string(data), tc.code) {
			t.Fatalf("response = %d %s %v", response.StatusCode, data, readErr)
		}
	}
	assertDirectoryEntries(t, directory, 0)
}

func TestMessageOversize(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint(chunked), func(t *testing.T) {
			directory := t.TempDir()
			server, _ := newDeliveryServer(t, directory, 0)
			payload := bytes.NewReader(bytes.Repeat([]byte("x"), int(messageBodyLimit)+1))
			request, err := http.NewRequest(http.MethodPost, server.URL+"/messages", payload)
			if err != nil {
				t.Fatal(err)
			}
			if chunked {
				request.ContentLength = -1
			}
			request.Header.Set("Content-Type", "application/octet-stream")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != 413 || !strings.Contains(string(data), "request_too_large") {
				t.Fatalf("response = %d %s %v", response.StatusCode, data, readErr)
			}
			assertDirectoryEntries(t, directory, 0)
		})
	}
}

func TestMessageDestinationFailures(t *testing.T) {
	t.Run("disappeared", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "mailbox")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		server, _ := newDeliveryServer(t, directory, 0)
		if err := os.Remove(directory); err != nil {
			t.Fatal(err)
		}
		status, _, body := deliveryRequest(t, server, "application/json", []byte("test"))
		if status != 500 || !strings.Contains(body, "delivery_failed") {
			t.Fatalf("response = %d %s", status, body)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatalf("directory recreated: %v", err)
		}
	})
	t.Run("existing filename", func(t *testing.T) {
		directory := t.TempDir()
		server, handler := newDeliveryServer(t, directory, 0)
		handler.filename = func() string { return "localpost_existing.dat" }
		target := filepath.Join(directory, handler.filename())
		if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		status, _, body := deliveryRequest(t, server, "application/json", []byte("replacement"))
		if status != 500 || !strings.Contains(body, "delivery_failed") {
			t.Fatalf("response = %d %s", status, body)
		}
		actual, err := os.ReadFile(target)
		if err != nil || string(actual) != "original" {
			t.Fatal("existing file overwritten")
		}
		assertDirectoryEntries(t, directory, 1)
	})
}

func deliveryConnection(t *testing.T, server *httptest.Server) net.Conn {
	t.Helper()
	connection, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return connection
}

func rawDeliveryResponse(t *testing.T, connection net.Conn, status int, code string) {
	t.Helper()
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != status || !strings.Contains(string(data), code) {
		t.Fatalf("response = %d %s %v", response.StatusCode, data, err)
	}
}

func TestMessageBrokenBody(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		abort      bool
	}{
		{"malformed chunk", "Transfer-Encoding: chunked\r\n\r\n1\r\na\r\ninvalid\r\n", false},
		{"aborted body", "Content-Length: 20\r\n\r\nshort", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			server, _ := newDeliveryServer(t, directory, 0)
			connection := deliveryConnection(t, server)
			if _, err := io.WriteString(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\n"+tc.wire); err != nil {
				t.Fatal(err)
			}
			if tc.abort {
				tcp, ok := connection.(*net.TCPConn)
				if !ok {
					t.Fatal("expected TCP connection")
				}
				if err := tcp.CloseWrite(); err != nil {
					t.Fatal(err)
				}
			}
			rawDeliveryResponse(t, connection, 400, "invalid_request")
			assertDirectoryEntries(t, directory, 0)
		})
	}
}

func TestMessageIdleDeadline(t *testing.T) {
	directory := t.TempDir()
	server, _ := newDeliveryServer(t, directory, 200*time.Millisecond)
	connection := deliveryConnection(t, server)
	if _, err := io.WriteString(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 20\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	rawDeliveryResponse(t, connection, 408, "request_timeout")
	assertDirectoryEntries(t, directory, 0)
}

func TestMessageProgressExtendsDeadline(t *testing.T) {
	directory := t.TempDir()
	server, _ := newDeliveryServer(t, directory, 300*time.Millisecond)
	connection := deliveryConnection(t, server)
	if _, err := io.WriteString(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 8\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err := io.WriteString(connection, "x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(70 * time.Millisecond)
	}
	rawDeliveryResponse(t, connection, 200, `"bytes":8`)
	assertDirectoryEntries(t, directory, 1)
}

func TestRegisteredMailboxDeliversSeparately(t *testing.T) {
	registry := newRegistry()
	defer registry.close()
	for range 2 {
		directory := t.TempDir()
		mailbox, err := registry.register(directory)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/messages", mailbox.Port), "application/octet-stream", strings.NewReader(directory))
		if err != nil {
			t.Fatal(err)
		}
		var receipt messageReceipt
		decodeErr := json.NewDecoder(response.Body).Decode(&receipt)
		_ = response.Body.Close()
		client.CloseIdleConnections()
		if decodeErr != nil || response.StatusCode != 200 {
			t.Fatalf("registered delivery = %d %v", response.StatusCode, decodeErr)
		}
		actual, err := os.ReadFile(filepath.Join(directory, receipt.Filename))
		if err != nil || string(actual) != directory {
			t.Fatal("wrong mailbox destination")
		}
	}
}

type failingMessageWriter struct{ short bool }

func (w failingMessageWriter) Write(data []byte) (int, error) {
	if w.short {
		return len(data) - 1, nil
	}
	return 0, os.ErrPermission
}

func TestMessageWriteFailures(t *testing.T) {
	for _, short := range []bool{false, true} {
		_, err := copyMessage(failingMessageWriter{short: short}, strings.NewReader("payload"), http.NewResponseController(httptest.NewRecorder()), time.Second)
		if !errors.Is(err, errMessageStorage) {
			t.Fatalf("write failure not classified as storage error: %v", err)
		}
	}
}

func TestMessageIncompleteNotPublished(t *testing.T) {
	directory := t.TempDir()
	server, _ := newDeliveryServer(t, directory, time.Second)
	connection := deliveryConnection(t, server)
	if _, err := io.WriteString(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 20\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	// Wait for the temporary file as evidence that the handler began processing.
	deadline := time.Now().Add(time.Second)
	for {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".dat") {
				t.Fatal("incomplete body published")
			}
		}
		if len(entries) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handler did not create temporary file")
		}
		time.Sleep(time.Millisecond)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	assertDirectoryEntries(t, directory, 0)
}

func TestMessageRejectedBodyClosesConnection(t *testing.T) {
	for _, framing := range []string{"Content-Length: 20\r\n", "Transfer-Encoding: chunked\r\n"} {
		for _, tc := range []struct {
			name, method, path, mediaType string
			status                        int
			code                          string
		}{
			{"timeout", "POST", "/messages", "application/json", 408, "request_timeout"},
			{"media type", "POST", "/messages", "text/plain", 415, "unsupported_media_type"},
			{"method", "PUT", "/messages", "application/json", 405, "method_not_allowed"},
			{"path", "POST", "/wrong", "application/json", 404, "not_found"},
		} {
			t.Run(tc.name+framing, func(t *testing.T) {
				directory := t.TempDir()
				server, _ := newDeliveryServer(t, directory, 50*time.Millisecond)
				connection := deliveryConnection(t, server)
				// The client deliberately sends only headers, leaves its write side open,
				// and waits for the server to close. Client cleanup must not mask a drain.
				wire := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\n%s\r\n", tc.method, tc.path, tc.mediaType, framing)
				if _, err := io.WriteString(connection, wire); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(connection)
				response, err := http.ReadResponse(reader, nil)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil || response.StatusCode != tc.status || !strings.Contains(string(body), tc.code) {
					t.Fatalf("response = %d %s %v", response.StatusCode, body, err)
				}
				if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
					t.Fatalf("server did not close rejected-body connection: %v", err)
				}
				assertDirectoryEntries(t, directory, 0)
			})
		}
	}
}

func TestMessageSuccessfulKeepAlive(t *testing.T) {
	directory := t.TempDir()
	server, _ := newDeliveryServer(t, directory, 50*time.Millisecond)
	connection := deliveryConnection(t, server)
	reader := bufio.NewReader(connection)
	for range 2 {
		if _, err := io.WriteString(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1\r\n\r\nx"); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || response.Close {
			t.Fatalf("successful connection not reusable: %d %v", response.StatusCode, readErr)
		}
		// Wait beyond the previous upload's idle window before the next request.
		time.Sleep(80 * time.Millisecond)
	}
	assertDirectoryEntries(t, directory, 2)
}
