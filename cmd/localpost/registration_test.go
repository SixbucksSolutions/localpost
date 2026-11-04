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
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func registrationRequest(t *testing.T, r http.Handler, directory string) (*httptest.ResponseRecorder, registration) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"directory": directory})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/registrations", bytes.NewReader(body))
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

func TestRegistrationListenersAndDirectories(t *testing.T) {
	r := newRegistry()
	defer r.close()
	firstDir, secondDir := t.TempDir(), t.TempDir()
	response, first := registrationRequest(t, r, firstDir)
	secondResponse, second := registrationRequest(t, r, secondDir)
	if response.Code != http.StatusOK || secondResponse.Code != http.StatusOK {
		t.Fatalf("registration statuses = %d, %d", response.Code, secondResponse.Code)
	}
	if first.ID == "" || first.ID == second.ID || first.Port == second.Port || first.Port < 1024 {
		t.Fatalf("invalid distinct registrations: %+v, %+v", first, second)
	}
	r.mu.Lock()
	firstDestination, secondDestination := r.registrations[first.ID].directory, r.registrations[second.ID].directory
	r.mu.Unlock()
	if firstDestination != firstDir || secondDestination != secondDir {
		t.Fatal("registration destinations were not retained separately")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, mailbox := range []registration{first, second} {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", mailbox.Port))
		if err != nil {
			t.Fatalf("returned mailbox was not reachable: %v", err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("mailbox status = %d", response.StatusCode)
		}
	}
	for _, directory := range []string{firstDir, secondDir} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("registration left files: %v, %v", entries, err)
		}
	}
	r.close()
	for _, mailbox := range []registration{first, second} {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", mailbox.Port))
		if err != nil {
			t.Fatalf("mailbox port leaked: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if registrationCount(r) != 0 {
		t.Fatal("closed registry retained mappings")
	}
}

func TestRegistrationRejectsDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"", "relative", filepath.Join(root, "missing"), file} {
		r := newRegistry()
		response, _ := registrationRequest(t, r, directory)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_directory") {
			t.Fatalf("directory %q returned %d %s", directory, response.Code, response.Body.String())
		}
		if registrationCount(r) != 0 {
			t.Fatal("failed validation created a mapping")
		}
		r.close()
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing directory was created")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("existing file was changed")
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
	r := newRegistry()
	defer r.close()
	response, _ := registrationRequest(t, r, directory)
	if response.Code != http.StatusBadRequest || registrationCount(r) != 0 {
		t.Fatalf("unwritable directory status = %d", response.Code)
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
	valid, err := json.Marshal(map[string]string{"directory": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, contentType, body string
		status                                int
		code                                  string
	}{
		{"no content type", "POST", "/registrations", "", string(valid), 415, "unsupported_media_type"},
		{"wrong content type", "POST", "/registrations", "text/plain", string(valid), 415, "unsupported_media_type"},
		{"malformed type", "POST", "/registrations", "application/json;broken", string(valid), 415, "unsupported_media_type"},
		{"empty", "POST", "/registrations", "application/json", "", 400, "invalid_request"},
		{"broken", "POST", "/registrations", "application/json", "{", 400, "invalid_request"},
		{"array", "POST", "/registrations", "application/json", "[]", 400, "invalid_request"},
		{"null", "POST", "/registrations", "application/json", "null", 400, "invalid_request"},
		{"unknown", "POST", "/registrations", "application/json", `{"directory":"x","extra":true}`, 400, "invalid_request"},
		{"type", "POST", "/registrations", "application/json", `{"directory":42}`, 400, "invalid_request"},
		{"trailing", "POST", "/registrations", "application/json", string(valid) + " {}", 400, "invalid_request"},
		{"trailing garbage", "POST", "/registrations", "application/json", string(valid) + " x", 400, "invalid_request"},
		{"oversized", "POST", "/registrations", "application/json", string(valid) + strings.Repeat(" ", registrationBodyLimit), 413, "request_too_large"},
		{"method", "GET", "/registrations", "application/json", "", 405, "method_not_allowed"},
		{"route", "POST", "/elsewhere", "application/json", "", 404, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry()
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

func TestRegistrationAllocationFailure(t *testing.T) {
	r := newRegistry()
	defer r.close()
	r.listen = func(string, string) (net.Listener, error) { return nil, errors.New("synthetic bind failure") }
	response, _ := registrationRequest(t, r, t.TempDir())
	if response.Code != http.StatusServiceUnavailable || registrationCount(r) != 0 {
		t.Fatalf("failed allocation = %d; mappings %d", response.Code, registrationCount(r))
	}
}

func TestRegistrationConcurrentClose(t *testing.T) {
	r := newRegistry()
	directory := t.TempDir()
	var workers sync.WaitGroup
	addresses := make(chan string, 32)
	for range 32 {
		workers.Go(func() {
			mailbox, err := r.register(directory)
			if err == nil {
				addresses <- fmt.Sprintf("127.0.0.1:%d", mailbox.Port)
			}
		})
	}
	workers.Go(r.close)
	workers.Wait()
	close(addresses)
	r.close()
	for address := range addresses {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			t.Errorf("concurrent close leaked %s: %v", address, err)
			continue
		}
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	}
	response, _ := registrationRequest(t, r, directory)
	if response.Code != http.StatusServiceUnavailable || registrationCount(r) != 0 {
		t.Fatal("closed registry admitted registration")
	}
}

func TestServeCancellationClosesMailbox(t *testing.T) {
	listener := newTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, listener) }()
	body, err := json.Marshal(map[string]string{"directory": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Post("http://"+listener.Addr().String()+"/registrations", "application/json", bytes.NewReader(body))
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
	rebound, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", mailbox.Port))
	if err != nil {
		t.Fatalf("shutdown leaked mailbox: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}

// badAddressListener simulates an allocation that violates our required port range.
type badAddressListener struct{ net.Listener }

func (badAddressListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }

func TestRegistrationRejectsInvalidAllocation(t *testing.T) {
	listener := newTestListener(t)
	address := listener.Addr().String()
	r := newRegistry()
	defer r.close()
	r.listen = func(string, string) (net.Listener, error) { return badAddressListener{listener}, nil }
	response, _ := registrationRequest(t, r, t.TempDir())
	if response.Code != http.StatusServiceUnavailable || registrationCount(r) != 0 {
		t.Fatal("invalid allocation was admitted")
	}
	rebound, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("invalid allocation leaked listener: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMailboxListenerFailureClosesConnections(t *testing.T) {
	r := newRegistry()
	defer r.close()
	_, result := registrationRequest(t, r, t.TempDir())
	r.mu.Lock()
	mailbox := r.registrations[result.ID]
	r.mu.Unlock()
	if mailbox == nil {
		t.Fatal("missing registered mailbox")
	}
	conn, err := net.DialTimeout("tcp4", mailbox.listener.Addr().String(), time.Second)
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
	_, readErr := io.Copy(io.Discard, response.Body)
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := mailbox.listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("connection survived listener failure: %v", err)
	}
}
