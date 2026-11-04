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
	"testing"
	"time"
)

func newHTTPEdgeAdmin(t *testing.T, timeout time.Duration) (*httptest.Server, *registry) {
	t.Helper()
	r := newRegistry()
	t.Cleanup(r.close)
	server := httptest.NewUnstartedServer(withCORS(r, "POST, DELETE, OPTIONS"))
	server.Config.ReadTimeout = timeout
	server.Start()
	t.Cleanup(server.Close)
	return server, r
}

func readHTTPEdgeResponse(t *testing.T, reader *bufio.Reader, status int, code string) bool {
	t.Helper()
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != status {
		t.Fatalf("response = %d %q, %v", response.StatusCode, body, readErr)
	}
	if code != "" {
		var result struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Content-Type") != "application/json" || result.Error.Code != code || result.Error.Message == "" {
			t.Fatalf("application error = %q, headers %v", body, response.Header)
		}
	}
	if response.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing permissive CORS")
	}
	return response.Close
}

func assertHTTPEdgeClosed(t *testing.T, connection net.Conn, reader *bufio.Reader) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("rejected connection remained open: %v", err)
	}
}

func TestHTTPAdminEarlyRejection(t *testing.T) {
	for _, framing := range []string{"Content-Length: 20\r\n", "Transfer-Encoding: chunked\r\n"} {
		for _, tc := range []struct {
			name, method, path, media string
			status                    int
			code                      string
		}{
			{"path", "POST", "/missing", "application/json", 404, "not_found"},
			{"method", "PUT", "/registrations", "application/json", 405, "method_not_allowed"},
			{"delete method", "POST", "/registrations/missing", "application/json", 405, "method_not_allowed"},
			{"media", "POST", "/registrations", "text/plain", 415, "unsupported_media_type"},
		} {
			t.Run(tc.name+framing, func(t *testing.T) {
				server, r := newHTTPEdgeAdmin(t, 2*time.Second)
				connection := deliveryConnection(t, server)
				if err := connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				if _, err := fmt.Fprintf(connection, "%s %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\n%s\r\n", tc.method, tc.path, tc.media, framing); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(connection)
				closed := readHTTPEdgeResponse(t, reader, tc.status, tc.code)
				if !closed {
					t.Fatal("rejection must close unread body connection")
				}
				assertHTTPEdgeClosed(t, connection, reader)
				if registrationCount(r) != 0 {
					t.Fatal("rejection registered mailbox")
				}
			})
		}
	}
}

func TestHTTPAdminStalledBody(t *testing.T) {
	for _, framing := range []string{"Content-Length: 20\r\n\r\n{", "Transfer-Encoding: chunked\r\n\r\n1\r\n{\r\n"} {
		t.Run(framing, func(t *testing.T) {
			server, r := newHTTPEdgeAdmin(t, 50*time.Millisecond)
			connection := deliveryConnection(t, server)
			if _, err := io.WriteString(connection, "POST /registrations HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\n"+framing); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(connection)
			closed := readHTTPEdgeResponse(t, reader, 400, "invalid_request")
			if !closed {
				t.Fatal("timed-out body must close connection")
			}
			assertHTTPEdgeClosed(t, connection, reader)
			if registrationCount(r) != 0 {
				t.Fatal("incomplete body registered mailbox")
			}
		})
	}
}

func TestHTTPExpectContinue(t *testing.T) {
	for _, surface := range []string{"admin", "mailbox"} {
		for _, oversized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s rejected oversized=%t", surface, oversized), func(t *testing.T) {
				directory := t.TempDir()
				var server *httptest.Server
				var r *registry
				path, length := "/messages", messageBodyLimit+1
				if surface == "admin" {
					server, r = newHTTPEdgeAdmin(t, 2*time.Second)
					path, length = "/registrations", registrationBodyLimit+1
				} else {
					server = httptest.NewServer(withCORS(newMessageHandler(directory), "POST, OPTIONS"))
					t.Cleanup(server.Close)
				}
				media, status, code := "application/json", 413, "request_too_large"
				if !oversized {
					media, length, status, code = "text/plain", 20, 415, "unsupported_media_type"
				}
				connection := deliveryConnection(t, server)
				if _, err := fmt.Fprintf(connection, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nExpect: 100-continue\r\nContent-Length: %d\r\n\r\n", path, media, length); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(connection)
				readHTTPEdgeResponse(t, reader, status, code)
				assertHTTPEdgeClosed(t, connection, reader)
				if r != nil && registrationCount(r) != 0 {
					t.Fatal("rejected body registered mailbox")
				}
				assertDirectoryEntries(t, directory, 0)
			})
		}
	}
	for _, surface := range []string{"admin", "mailbox"} {
		t.Run(surface, func(t *testing.T) {
			directory := t.TempDir()
			var server *httptest.Server
			var payload, path string
			if surface == "admin" {
				server, _ = newHTTPEdgeAdmin(t, time.Second)
				data, err := json.Marshal(map[string]string{"directory": directory})
				if err != nil {
					t.Fatal(err)
				}
				payload, path = string(data), "/registrations"
			} else {
				server = httptest.NewServer(withCORS(newMessageHandler(directory), "POST, OPTIONS"))
				t.Cleanup(server.Close)
				payload, path = "opaque", "/messages"
			}
			connection := deliveryConnection(t, server)
			if _, err := fmt.Fprintf(connection, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nExpect: 100-continue\r\nContent-Length: %d\r\n\r\n", path, len(payload)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(connection)
			interim, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = interim.Body.Close()
			if interim.StatusCode != 100 {
				t.Fatalf("expected continue, got %d", interim.StatusCode)
			}
			if _, err := io.WriteString(connection, payload); err != nil {
				t.Fatal(err)
			}
			if closed := readHTTPEdgeResponse(t, reader, 200, ""); closed {
				t.Fatal("successful body prevented reuse")
			}
			// Reuse the same connection with chunked framing after an Expect exchange.
			if _, err := fmt.Fprintf(connection, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", path, len(payload), payload); err != nil {
				t.Fatal(err)
			}
			if closed := readHTTPEdgeResponse(t, reader, 200, ""); closed {
				t.Fatal("chunked request prevented reuse")
			}
		})
	}
}

func TestHTTPParserRefusals(t *testing.T) {
	for _, surface := range []string{"admin", "mailbox"} {
		for _, tc := range []struct {
			name, headers string
			status        int
		}{
			{"conflicting lengths", "Content-Length: 1\r\nContent-Length: 2\r\n", 400},
			{"invalid length", "Content-Length: nope\r\n", 400},
			{"unsupported transfer", "Transfer-Encoding: gzip\r\n", 501},
		} {
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				directory := t.TempDir()
				var server *httptest.Server
				var r *registry
				if surface == "admin" {
					server, r = newHTTPEdgeAdmin(t, time.Second)
				} else {
					server, _ = newDeliveryServer(t, directory, 0)
				}
				connection := deliveryConnection(t, server)
				if _, err := io.WriteString(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\n"+tc.headers+"\r\n"); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(connection)
				response, err := http.ReadResponse(reader, nil)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				// Parser refusals belong to net/http, before application JSON and CORS.
				if readErr != nil || response.StatusCode != tc.status || !response.Close || json.Valid(body) || response.Header.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("parser response = %d %q %v", response.StatusCode, body, readErr)
				}
				assertHTTPEdgeClosed(t, connection, reader)
				if r != nil && registrationCount(r) != 0 {
					t.Fatal("parser refusal registered mailbox")
				}
				assertDirectoryEntries(t, directory, 0)
			})
		}
	}
}

func TestHTTPOpaqueContentEncodingAndChunkedReuse(t *testing.T) {
	directory := t.TempDir()
	server, _ := newDeliveryServer(t, directory, 50*time.Millisecond)
	connection := deliveryConnection(t, server)
	reader := bufio.NewReader(connection)
	for _, encoding := range []string{"gzip", "unknown-coding"} {
		payload := []byte{0, 255, 'x', 13, 10}
		if _, err := fmt.Fprintf(connection, "POST /messages HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Encoding: %s\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n", encoding, len(payload)); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Write(payload); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(connection, "\r\n0\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		var receipt messageReceipt
		decodeErr := json.NewDecoder(response.Body).Decode(&receipt)
		_ = response.Body.Close()
		if decodeErr != nil || response.StatusCode != 200 || response.Close || receipt.Bytes != int64(len(payload)) {
			t.Fatalf("encoded delivery = %d %+v %v", response.StatusCode, receipt, decodeErr)
		}
		actual, err := os.ReadFile(filepath.Join(directory, receipt.Filename))
		if err != nil || !bytes.Equal(actual, payload) {
			t.Fatalf("encoding changed opaque bytes: %v", err)
		}
		time.Sleep(80 * time.Millisecond)
	}
	assertDirectoryEntries(t, directory, 2)
}
