package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A destination reused after DELETE must acquire a new identity. Requests for
// the old identity, including differently cased spellings, cannot reach it.
func TestRouteQualificationIdentityIsolation(t *testing.T) {
	server, r := newHTTPEdgeAdmin(t, time.Second)
	connection := deliveryConnection(t, server)
	reader := bufio.NewReader(connection)
	directories := []string{t.TempDir(), t.TempDir()}
	retained := []map[string][]byte{{}, {}}
	register := func(directory string) string {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"directory": directory})
		if err != nil {
			t.Fatal(err)
		}
		body := routeQualificationExchange(t, connection, reader, http.MethodPost, "/registrations", "application/json", payload)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := json.Unmarshal(fields["id"], &id); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 1 || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
			t.Fatalf("registration must contain only a canonical UUIDv4 id: %s", body)
		}
		return id
	}
	first, sibling := register(directories[0]), register(directories[1])
	deliver := func(id string, destination int, media string, payload []byte) {
		t.Helper()
		body := routeQualificationExchange(t, connection, reader, http.MethodPost, messagePath(id), media, payload)
		var receipt messageReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Bytes != int64(len(payload)) || !regexp.MustCompile(`^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$`).MatchString(receipt.Filename) {
			t.Fatalf("invalid UUIDv7 delivery receipt: %s", body)
		}
		if _, duplicate := retained[destination][receipt.Filename]; duplicate {
			t.Fatal("delivery reused a filename")
		}
		retained[destination][receipt.Filename] = payload
	}
	deliver(strings.ToUpper(first), 0, "application/json", []byte("{invalid JSON\x00\xff"))
	deliver(sibling, 1, "application/octet-stream", []byte{0, 255, '\r', '\n'})
	body := routeQualificationExchange(t, connection, reader, http.MethodDelete, "/registrations/"+strings.ToUpper(first), "", nil)
	var deleted struct {
		ID           string `json:"id"`
		Unregistered bool   `json:"unregistered"`
		Forced       bool   `json:"forced"`
	}
	if err := json.Unmarshal(body, &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.ID != first || !deleted.Unregistered || deleted.Forced {
		t.Fatalf("DELETE did not return canonical completed identity: %s", body)
	}
	replacement := register(directories[0])
	if replacement == first || replacement == sibling {
		t.Fatal("new registration reused an existing or retired identity")
	}

	for _, tc := range []struct {
		name, id, code string
		status         int
	}{
		{"unknown", "12345678-1234-4234-8234-123456789abc", "registration_not_found", 404},
		{"malformed", "not-a-uuid", "invalid_registration_id", 400},
		{"wrong version", "12345678-1234-7234-8234-123456789abc", "invalid_registration_id", 400},
		{"retired", first, "registration_not_found", 404},
		{"retired uppercase", strings.ToUpper(first), "registration_not_found", 404},
	} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				conn := deliveryConnection(t, server)
				path := "/registrations/" + tc.id
				if method == http.MethodPost {
					path += "/messages"
				}
				if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: localhost\r\nContent-Length: 0\r\n\r\n", method, path); err != nil {
					t.Fatal(err)
				}
				readHTTPEdgeResponse(t, bufio.NewReader(conn), tc.status, tc.code)
				if registrationCount(r) != 2 || lifecycleMailbox(r, replacement) == nil || lifecycleMailbox(r, sibling) == nil {
					t.Fatal("invalid identity changed live registrations")
				}
				for i, directory := range directories {
					qualificationFiles(t, directory, retained[i])
				}
			})
		}
	}
	// The original TCP connection remains usable across both registrations,
	// DELETE and replacement; unrelated rejected connections cannot poison it.
	deliver(replacement, 0, "application/octet-stream", []byte("replacement"))
	deliver(strings.ToUpper(sibling), 1, "application/json", nil)
	for i, directory := range directories {
		qualificationFiles(t, directory, retained[i])
	}
}

// Rejected and preflight requests must not leave body bytes to be interpreted
// as a subsequent request, nor let a stalled body tie up the shared listener.
func TestRouteQualificationEarlyBodyCleanup(t *testing.T) {
	server, r := newHTTPEdgeAdmin(t, 2*time.Second)
	directories := []string{t.TempDir(), t.TempDir()}
	liveResponse, live := registrationRequest(t, r, directories[0])
	retiredResponse, retired := registrationRequest(t, r, directories[1])
	if liveResponse.Code != http.StatusOK || retiredResponse.Code != http.StatusOK {
		t.Fatal("unable to establish cleanup test registrations")
	}
	r.unregister(httptest.NewRecorder(), retired.ID)
	for _, framing := range []string{"Content-Length: 4096\r\n\r\nx", "Transfer-Encoding: chunked\r\n\r\n1\r\nx\r\n"} {
		for _, tc := range []struct {
			name, method, path, media, code string
			status                          int
		}{
			{"unknown upload", "POST", messagePath("12345678-1234-4234-8234-123456789abc"), "application/json", "registration_not_found", 404},
			{"malformed delete", "DELETE", "/registrations/not-a-uuid", "application/json", "invalid_registration_id", 400},
			{"retired upload", "POST", messagePath(retired.ID), "application/json", "registration_not_found", 404},
			{"live wrong method", "PUT", messagePath(live.ID), "application/json", "method_not_allowed", 405},
			{"live wrong media", "POST", messagePath(live.ID), "text/plain", "unsupported_media_type", 415},
			{"live preflight", "OPTIONS", "/registrations/" + live.ID, "application/json", "", 204},
			{"retired preflight", "OPTIONS", messagePath(retired.ID), "application/json", "", 204},
		} {
			t.Run(tc.name+"/"+strings.Split(framing, ":")[0], func(t *testing.T) {
				conn := deliveryConnection(t, server)
				if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				// If cleanup accidentally dispatches these body bytes, the live
				// registration disappears and the isolation assertions fail.
				pipeline := "DELETE /registrations/" + live.ID + " HTTP/1.1\r\nHost: localhost\r\n\r\n"
				if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nOrigin: https://arbitrary.example\r\n%s%s", tc.method, tc.path, tc.media, framing, pipeline); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(conn)
				if !readHTTPEdgeResponse(t, reader, tc.status, tc.code) {
					t.Fatal("unconsumed body remained reusable")
				}
				assertHTTPEdgeClosed(t, conn, reader)
				if registrationCount(r) != 1 || lifecycleMailbox(r, live.ID) == nil {
					t.Fatal("body cleanup changed registration ownership")
				}
				for _, directory := range directories {
					assertDirectoryEntries(t, directory, 0)
				}
			})
		}
	}
	conn := deliveryConnection(t, server)
	routeQualificationExchange(t, conn, bufio.NewReader(conn), http.MethodPost, messagePath(live.ID), "application/octet-stream", []byte("still available"))
	assertDirectoryEntries(t, directories[0], 1)
	assertDirectoryEntries(t, directories[1], 0)
}

func routeQualificationExchange(t *testing.T, conn net.Conn, reader *bufio.Reader, method, path, media string, payload []byte) []byte {
	t.Helper()
	if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nContent-Length: %d\r\nOrigin: https://arbitrary.example\r\n\r\n", method, path, media, len(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || response.Close {
		t.Fatalf("shared connection exchange failed: status %d, close %t, read %v, close %v", response.StatusCode, response.Close, readErr, closeErr)
	}
	if response.Header.Get("Content-Type") != "application/json" || response.Header.Get("Access-Control-Allow-Origin") != "*" || response.Header.Get("Access-Control-Allow-Methods") != "POST, DELETE, OPTIONS" || response.Header.Get("Access-Control-Allow-Headers") != "Content-Type" || response.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("unexpected response policy: %v", response.Header)
	}
	return body
}
