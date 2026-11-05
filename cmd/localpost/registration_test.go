package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func registrationRequest(t *testing.T, r http.Handler) (*httptest.ResponseRecorder, registration) {
	t.Helper()
	body, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/registrations", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	var result registration
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return response, result
}

func TestRegistrationIDsAndDirectories(t *testing.T) {
	r := newRegistry(t.TempDir())
	defer r.close()
	response, first := registrationRequest(t, r)
	secondResponse, second := registrationRequest(t, r)
	if response.Code != http.StatusOK || secondResponse.Code != http.StatusOK {
		t.Fatalf("registration statuses = %d, %d", response.Code, secondResponse.Code)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(first.ID) || !pattern.MatchString(second.ID) || first.ID == second.ID {
		t.Fatalf("invalid distinct registrations: %s, %s", first.ID, second.ID)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields["id"] == nil || fields["directory"] == nil {
		t.Fatalf("registration response fields = %v", fields)
	}
	r.mu.Lock()
	firstDestination, secondDestination := r.registrations[first.ID].Directory, r.registrations[second.ID].Directory
	r.mu.Unlock()
	if firstDestination != filepath.Join(r.root, first.ID) || secondDestination != filepath.Join(r.root, second.ID) || first.Directory != firstDestination || second.Directory != secondDestination {
		t.Fatal("registration destinations were not retained separately")
	}
	for _, directory := range []string{firstDestination, secondDestination} {
		assertDirectoryEntries(t, directory, 0)
	}
	r.close()
	if registrationCount(r) != 0 {
		t.Fatal("closed registry retained mappings")
	}
}

func TestRegistrationRouteIDs(t *testing.T) {
	r := newRegistry(t.TempDir())
	defer r.close()
	for _, id := range []string{"invalid", "12345678-1234-7234-8234-123456789abc", "12345678-1234-4234-7234-123456789abc"} {
		for _, suffix := range []string{"", "/messages"} {
			method := http.MethodDelete
			if suffix != "" {
				method = http.MethodPost
			}
			response := httptest.NewRecorder()
			r.ServeHTTP(response, httptest.NewRequest(method, apiPrefix+"/registrations/"+id+suffix, nil))
			if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_registration_id") {
				t.Fatalf("invalid ID route: %d %s", response.Code, response.Body.String())
			}
		}
	}
	for _, path := range []string{apiPrefix + "/registrations/12345678-1234-4234-8234-123456789abc", apiPrefix + "/registrations/12345678-1234-4234-8234-123456789abc/messages", "/messages"} {
		response := httptest.NewRecorder()
		method := http.MethodPost
		code := "registration_not_found"
		if path == "/messages" {
			code = "not_found"
		} else if !strings.HasSuffix(path, "/messages") {
			method = http.MethodDelete
		}
		r.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != 404 || !strings.Contains(response.Body.String(), code) {
			t.Fatalf("missing ID route: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestMailboxRootRejectsInvalidDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"", "relative", filepath.Join(root, "missing"), file} {
		if _, err := prepareMailboxRoot(directory); err == nil {
			t.Fatalf("invalid root %q accepted", directory)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing root created")
	}
}

func TestRegistrationUnwritableDirectory(t *testing.T) {
	directory := t.TempDir()
	if runtime.GOOS == "windows" {
		current, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		sid := "*" + current.Uid
		if err := exec.Command("icacls", directory, "/deny", sid+":(WD)").Run(); err != nil {
			t.Fatalf("deny temporary directory file creation: %v", err)
		}
		t.Cleanup(func() {
			if err := exec.Command("icacls", directory, "/remove:d", sid).Run(); err != nil {
				t.Errorf("restore temporary directory ACL: %v", err)
			}
		})
	} else {
		if err := os.Chmod(directory, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(directory, 0700); err != nil {
				t.Error(err)
			}
		})
	}
	probe, err := os.CreateTemp(directory, "permission-check-*")
	if err == nil {
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(probe.Name()); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			t.Fatal("temporary directory ACL did not deny file creation")
		}
		t.Skip("test user bypasses directory write restrictions")
	}
	if _, err := prepareMailboxRoot(directory); err == nil {
		t.Fatal("unwritable root accepted")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed validation left files: %v, %v", entries, err)
	}
}

func registrationCount(r *registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.registrations)
}
func TestRegistrationRequestValidation(t *testing.T) {
	valid, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, contentType, body string
		status                                int
		code                                  string
	}{
		{"no content type", "POST", apiPrefix + "/registrations", "", string(valid), 415, "unsupported_media_type"},
		{"wrong content type", "POST", apiPrefix + "/registrations", "text/plain", string(valid), 415, "unsupported_media_type"},
		{"malformed type", "POST", apiPrefix + "/registrations", "application/json;broken", string(valid), 415, "unsupported_media_type"},
		{"empty", "POST", apiPrefix + "/registrations", "application/json", "", 400, "invalid_request"},
		{"broken", "POST", apiPrefix + "/registrations", "application/json", "{", 400, "invalid_request"},
		{"array", "POST", apiPrefix + "/registrations", "application/json", "[]", 400, "invalid_request"},
		{"null", "POST", apiPrefix + "/registrations", "application/json", "null", 400, "invalid_request"},
		{"unknown", "POST", apiPrefix + "/registrations", "application/json", `{"directory":"x","extra":true}`, 400, "invalid_request"},
		{"former directory", "POST", apiPrefix + "/registrations", "application/json", `{"directory":"x"}`, 400, "invalid_request"},
		{"type", "POST", apiPrefix + "/registrations", "application/json", `{"directory":42}`, 400, "invalid_request"},
		{"trailing", "POST", apiPrefix + "/registrations", "application/json", string(valid) + " {}", 400, "invalid_request"},
		{"trailing garbage", "POST", apiPrefix + "/registrations", "application/json", string(valid) + " x", 400, "invalid_request"},
		{"oversized", "POST", apiPrefix + "/registrations", "application/json", string(valid) + strings.Repeat(" ", registrationBodyLimit), 413, "request_too_large"},
		{"method", "GET", apiPrefix + "/registrations", "application/json", "", 405, "method_not_allowed"},
		{"route", "POST", "/elsewhere", "application/json", "", 404, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry(t.TempDir())
			defer r.close()
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)
			if response.Code != tc.status || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			var result struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Error.Code != tc.code || result.Error.Message == "" {
				t.Fatalf("error = %+v", result)
			}
			if registrationCount(r) != 0 {
				t.Fatal("invalid request created a mapping")
			}
		})
	}
}

func TestRegistrationConcurrentClose(t *testing.T) {
	r := newRegistry(t.TempDir())
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() { _, _ = r.register() })
	}
	workers.Go(r.close)
	workers.Wait()
	r.close()
	response, _ := registrationRequest(t, r)
	if response.Code != http.StatusServiceUnavailable || registrationCount(r) != 0 {
		t.Fatal("closed registry admitted registration")
	}
}

func TestServeCancellationClosesMailbox(t *testing.T) {
	listener := newTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, listener, t.TempDir()) }()
	body, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Post("http://"+listener.Addr().String()+apiPrefix+"/registrations", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("registration = %d %s %v", response.StatusCode, data, readErr)
	}
	var mailbox registration
	if err := json.Unmarshal(data, &mailbox); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stalled")
	}
	rebound, err := net.Listen("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatalf("shutdown leaked mailbox: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedListenerFailureClosesConnections(t *testing.T) {
	listener := newTestListener(t)
	r := newRegistry(t.TempDir())
	r.gracePeriod = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- serveWithRegistry(context.Background(), listener, r) }()
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
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
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("listener failure returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener failure stalled shutdown")
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("connection survived listener failure: %v", err)
	}
	if registrationCount(r) != 0 {
		t.Fatal("listener failure retained registrations")
	}
}
