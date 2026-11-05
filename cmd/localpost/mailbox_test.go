package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMailboxAllocationCollisions(t *testing.T) {
	for _, kind := range []string{"directory", "file", "active"} {
		t.Run(kind, func(t *testing.T) {
			r := newRegistry(t.TempDir())
			defer r.close()
			const collision = "12345678-1234-4234-8234-123456789abc"
			const available = "22345678-1234-4234-8234-123456789abc"
			path := filepath.Join(r.root, collision)
			r.newID = func() string { return collision }
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(path, []byte("preserved"), 0600); err != nil {
					t.Fatal(err)
				}
			case "active":
				if _, err := r.register(); err != nil {
					t.Fatal(err)
				}
			}
			attempts := 0
			r.newID = func() string {
				attempts++
				if attempts == 2 {
					return available
				}
				return collision
			}
			mailbox, err := r.register()
			if err != nil || mailbox.ID != available || attempts != 2 {
				t.Fatalf("collision recovery = %v, %v, attempts %d", mailbox, err, attempts)
			}
			if kind == "file" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "preserved" {
					t.Fatal("collision changed existing file")
				}
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("collision removed existing path")
			}
		})
	}
}

func TestMailboxAllocationFailureReleasesCapacity(t *testing.T) {
	for _, kind := range []string{"filesystem", "collisions"} {
		t.Run(kind, func(t *testing.T) {
			r := newRegistry(t.TempDir())
			defer r.close()
			r.registrationLimit = 1
			mkdir := r.mkdir
			calls := 0
			r.mkdir = func(_ string, _ os.FileMode) error {
				calls++
				if kind == "collisions" {
					return os.ErrExist
				}
				return os.ErrPermission
			}
			response, _ := registrationRequest(t, r)
			expectedCalls := 1
			if kind == "collisions" {
				expectedCalls = mailboxAllocationAttempts
			}
			if response.Code != 500 || !strings.Contains(response.Body.String(), `"code":"mailbox_allocation_failed"`) || calls != expectedCalls || registrationCount(r) != 0 {
				t.Fatalf("allocation failure = %d, attempts %d, registrations %d", response.Code, calls, registrationCount(r))
			}
			assertDirectoryEntries(t, r.root, 0)
			r.mkdir = mkdir
			response, _ = registrationRequest(t, r)
			if response.Code != 200 || registrationCount(r) != 1 {
				t.Fatal("failed allocation consumed capacity")
			}
		})
	}
}

func TestMailboxVisibilityAfterCreation(t *testing.T) {
	r := newRegistry(t.TempDir())
	defer r.close()
	entered, release := make(chan struct{}), make(chan struct{})
	r.mkdir = func(path string, mode os.FileMode) error {
		close(entered)
		<-release
		return os.Mkdir(path, mode)
	}
	done := make(chan error, 1)
	go func() { _, err := r.register(); done <- err }()
	<-entered
	// Allocation owns the registry lock until the filesystem operation completes.
	// No request can observe a registration whose directory has not been created.
	if r.mu.TryLock() {
		r.mu.Unlock()
		close(release)
		t.Fatal("allocation released registry ownership before creation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if registrationCount(r) != 1 {
		t.Fatal("successful allocation was not published")
	}
}

func TestMailboxRootPreparation(t *testing.T) {
	root := t.TempDir()
	actual, err := prepareMailboxRoot(root)
	if err != nil || !filepath.IsAbs(actual) {
		t.Fatalf("prepare root = %q, %v", actual, err)
	}
	assertDirectoryEntries(t, root, 0)
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic link privilege is unavailable")
		}
		t.Fatal(err)
	}
	resolved, err := prepareMailboxRoot(link)
	if err != nil || resolved != actual {
		t.Fatalf("canonical root = %q, %v", resolved, err)
	}
	assertDirectoryEntries(t, root, 0)
}

func TestRunRequiresMailboxRootBeforeListening(t *testing.T) {
	listener := newTestListener(t)
	// The occupied port must never be reached when root validation fails.
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener did not provide a TCP address")
	}
	cfg := config{port: address.Port}
	err := run(context.Background(), cfg, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "mailbox root:") {
		t.Fatalf("missing root = %v", err)
	}
	cfg.mailboxRoot = filepath.Join(t.TempDir(), "missing")
	err = run(context.Background(), cfg, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "mailbox root:") {
		t.Fatalf("absent root = %v", err)
	}
}

func TestMailboxRetirementRetentionAndPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(root)
	mailbox, err := r.register()
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	server := httptest.NewServer(r)
	defer server.Close()
	response, err := server.Client().Post(server.URL+messagePath(mailbox.ID), "application/octet-stream", strings.NewReader("retained"))
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("delivery = %d", response.StatusCode)
	}
	files, err := os.ReadDir(mailbox.Directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("delivered files = %v, %v", files, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(mailbox.Directory)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("mailbox permissions = %v, %v", info, err)
		}
		fileInfo, err := files[0].Info()
		if err != nil || fileInfo.Mode().Perm() != 0600 {
			t.Fatalf("delivery permissions = %v, %v", fileInfo, err)
		}
	}
	r.unregister(httptest.NewRecorder(), mailbox.ID)
	r.close()
	// Registry replacement alone has no startup side effects. Actual daemon
	// startup cleanup is covered through run in startup_test.go.
	replacement := newRegistry(root)
	defer replacement.close()
	missing := httptest.NewRecorder()
	replacement.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, apiPrefix+"/registrations/"+mailbox.ID, nil))
	if missing.Code != 404 {
		t.Fatal("registry replacement restored an old registration")
	}
	data, err := os.ReadFile(filepath.Join(mailbox.Directory, files[0].Name()))
	if err != nil || string(data) != "retained" {
		t.Fatal("retirement removed delivered bytes")
	}
	next, err := replacement.register()
	if err != nil || next.Directory == mailbox.Directory {
		t.Fatal("registry replacement adopted a retained mailbox")
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		t.Fatal("root was removed")
	}
}
