package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAddress(t *testing.T) {
	for _, port := range []int{-1, 0, 1023, 65536} {
		if _, err := address(port); err == nil {
			t.Fatalf("address(%d) accepted invalid port", port)
		}
	}
	got, err := address(53148)
	if err != nil || got != "127.0.0.1:53148" {
		t.Fatalf("address = %q, %v", got, err)
	}
}

func TestHandler(t *testing.T) {
	for _, tc := range []struct {
		method, path, contentType, contains string
		status                              int
	}{
		{http.MethodGet, "/", "text/html; charset=utf-8", "Add one", 200},
		{http.MethodGet, "/pilot.mjs", "text/javascript; charset=utf-8", "COUNTER_STEP = 1", 200},
		{http.MethodHead, "/", "text/html; charset=utf-8", "", 200},
		{http.MethodGet, "/README.md", "", "", 404},
		{http.MethodGet, "/main.go", "", "", 404},
		{http.MethodGet, "/pilot.test.mjs", "", "", 404},
		{http.MethodGet, "/../index.html", "", "", 404},
		{http.MethodPost, "/", "", "", 405},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler().ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, http.NoBody))
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
			if tc.contentType != "" && recorder.Header().Get("Content-Type") != tc.contentType {
				t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
			}
			if !strings.Contains(recorder.Body.String(), tc.contains) {
				t.Fatalf("missing expected asset content %q", tc.contains)
			}
			if tc.method == http.MethodHead && (recorder.Body.Len() != 0 || recorder.Header().Get("Content-Length") == "") {
				t.Fatal("HEAD must describe the asset without sending a body")
			}
			if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing static response protections")
			}
			if tc.status == 405 && recorder.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
		})
	}
}

func TestRunRejectsInvalidPort(t *testing.T) {
	if err := run(context.Background(), 0); err == nil {
		t.Fatal("run accepted port zero")
	}
}

func TestServeCancellationReleasesOwnedListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		// Best-effort cleanup of only this test's owned listener on failure.
		_ = listener.Close()
		t.Fatal("serve did not stop after cancellation")
	}
	rebound, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatalf("listener not released: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}
