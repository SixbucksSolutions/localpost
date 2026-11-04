package main

import (
	"bufio"
	"bytes"
	"context"
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

func corsRequest(t *testing.T, client *http.Client, method, url, contentType string, body []byte) ([]byte, http.Header, int) {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://arbitrary.example")
	request.Header.Set("Content-Type", contentType)
	if method == http.MethodOptions {
		request.Header.Set("Access-Control-Request-Method", "POST")
		request.Header.Set("Access-Control-Request-Headers", "content-type")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("response read: %v; close: %v", err, closeErr)
	}
	if response.Header.Get("Access-Control-Allow-Origin") != "*" || response.Header.Get("Access-Control-Allow-Headers") != "Content-Type" || response.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("unexpected CORS headers: %v", response.Header)
	}
	return data, response.Header, response.StatusCode
}

func assertPortClosed(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("listener still reachable: %s", address)
	}
}

func TestCORSJoinedCoreLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := newRegistry()
	done := make(chan error, 1)
	go func() { done <- serveWithRegistry(ctx, listener, registry) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server shutdown timed out")
		}
	})
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	admin := "http://" + listener.Addr().String()
	dirs := []string{t.TempDir(), t.TempDir()}
	mailboxes := make([]registration, 2)
	for i, dir := range dirs {
		body, err := json.Marshal(map[string]string{"directory": dir})
		if err != nil {
			t.Fatal(err)
		}
		data, _, status := corsRequest(t, client, "POST", admin+"/registrations", "application/json", body)
		if status != http.StatusOK {
			t.Fatalf("register status %d: %s", status, data)
		}
		if err := json.Unmarshal(data, &mailboxes[i]); err != nil {
			t.Fatal(err)
		}
	}
	targets := []struct{ url, methods string }{
		{admin + "/registrations", "POST, DELETE, OPTIONS"},
		{admin + "/registrations/" + mailboxes[0].ID, "POST, DELETE, OPTIONS"},
		{fmt.Sprintf("http://127.0.0.1:%d/messages", mailboxes[0].Port), "POST, OPTIONS"},
	}
	for _, target := range targets {
		data, headers, status := corsRequest(t, client, "OPTIONS", target.url, "", nil)
		if status != http.StatusNoContent || len(data) != 0 || headers.Get("Access-Control-Allow-Methods") != target.methods {
			t.Fatalf("preflight status %d, body %q, headers %v", status, data, headers)
		}
	}
	if registrationCount(registry) != 2 {
		t.Fatal("preflight changed registrations")
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("preflight wrote files: %v, %v", entries, err)
		}
	}
	for _, target := range []string{admin + "/missing", targets[2].url + "/missing"} {
		_, _, status := corsRequest(t, client, "POST", target, "application/json", nil)
		if status != http.StatusNotFound {
			t.Fatalf("missing route status %d", status)
		}
	}
	_, _, status := corsRequest(t, client, "POST", admin+"/registrations", "application/json", []byte("{"))
	if status != http.StatusBadRequest {
		t.Fatalf("invalid registration status %d", status)
	}
	deliver := func(index int, contentType string, payload []byte) {
		t.Helper()
		url := fmt.Sprintf("http://127.0.0.1:%d/messages", mailboxes[index].Port)
		data, _, status := corsRequest(t, client, "POST", url, contentType, payload)
		if status != http.StatusOK {
			t.Fatalf("delivery status %d: %s", status, data)
		}
		var receipt messageReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(dirs[index], receipt.Filename))
		if err != nil || !bytes.Equal(actual, payload) || receipt.Bytes != int64(len(payload)) {
			t.Fatalf("delivery mismatch: %v, receipt %+v", err, receipt)
		}
	}
	deliver(0, "application/octet-stream", []byte{0, 255, 1, 13, 10})
	deliver(1, "application/json", []byte("{invalid JSON"))
	deliver(1, "application/json", nil)
	_, _, status = corsRequest(t, client, "DELETE", targets[1].url, "", nil)
	if status != http.StatusOK {
		t.Fatalf("unregister status %d", status)
	}
	assertPortClosed(t, fmt.Sprintf("127.0.0.1:%d", mailboxes[0].Port))
	deliver(1, "application/octet-stream", []byte("sibling remains available"))
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		done <- nil
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	assertPortClosed(t, listener.Addr().String())
	assertPortClosed(t, fmt.Sprintf("127.0.0.1:%d", mailboxes[1].Port))
	if registrationCount(registry) != 0 {
		t.Fatal("shutdown retained registrations")
	}
}

func TestCORSIncompletePreflightBody(t *testing.T) {
	registry := newRegistry()
	defer registry.close()
	_, mailbox := registrationRequest(t, registry, t.TempDir())
	admin, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveWithRegistry(ctx, admin, registry) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for _, address := range []string{admin.Addr().String(), fmt.Sprintf("127.0.0.1:%d", mailbox.Port)} {
		for _, framing := range []string{
			"Content-Length: 4\r\n\r\nx",
			"Transfer-Encoding: chunked\r\nTrailer: X-Preflight\r\n\r\n4\r\nx",
			"Transfer-Encoding: chunked\r\nTrailer: X-Preflight\r\n\r\n0\r\nX-Preflight:",
		} {
			conn, err := net.DialTimeout("tcp4", address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			_, err = fmt.Fprintf(conn, "OPTIONS /messages HTTP/1.1\r\nHost: %s\r\nOrigin: null\r\n%s", address, framing)
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("preflight response: %v", response)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = reader.ReadByte()
			if !errors.Is(err, io.EOF) {
				t.Fatalf("incomplete preflight did not close connection: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if registrationCount(registry) != 1 {
		t.Fatal("incomplete preflight changed registry")
	}
	entries, err := os.ReadDir(registry.registrations[mailbox.ID].directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("incomplete preflight created files: %v, %v", entries, err)
	}
}

func TestCORSOriginUnrestricted(t *testing.T) {
	for _, origin := range []string{"null", "https://unlisted.example", "http://localhost:9999", ""} {
		request, err := http.NewRequest("OPTIONS", "http://localhost/messages", strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		// Real HTTP tests above cover the writer/controller contract.
		handler := withCORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("OPTIONS reached application") }), "POST, OPTIONS")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("origin %q rejected", origin)
		}
	}
}
