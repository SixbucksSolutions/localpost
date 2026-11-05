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
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMailboxStartupHelper(t *testing.T) {
	if os.Getenv("LOCALPOST_STARTUP_HELPER") != "1" {
		return
	}
	port, err := strconv.Atoi(os.Getenv("LOCALPOST_STARTUP_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), config{port: port, mailboxRoot: os.Getenv("LOCALPOST_STARTUP_ROOT")}, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

func startupCommand(ctx context.Context, root string, port int) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMailboxStartupHelper$")
	cmd.Env = append(os.Environ(), "LOCALPOST_STARTUP_HELPER=1", "LOCALPOST_STARTUP_ROOT="+root, "LOCALPOST_STARTUP_PORT="+strconv.Itoa(port))
	return cmd
}

func startStartupProcess(t *testing.T, root string, port int) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := startupCommand(ctx, root, port)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		// This helper deliberately dies without running daemon defers.
		_ = cmd.Wait()
	})
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	var output strings.Builder
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("startup exited before listening: %s", output.String())
			}
			// strings.Builder writes cannot fail.
			_, _ = output.WriteString(line + "\n")
			if strings.Contains(line, "localpost listening on") {
				return output.String()
			}
		case <-timer.C:
			t.Fatalf("startup did not become ready: %s", output.String())
		}
	}
}

func unusedStartupPort(t *testing.T) int {
	t.Helper()
	listener := newTestListener(t)
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("test listener has no TCP address")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address.Port
}

func TestRunStartupCleanupAfterKilledProcess(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old-mailbox", "nested")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(old, "retained.dat"), filepath.Join(root, "unfinished.tmp")} {
		if err := os.WriteFile(path, []byte("old payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	port := unusedStartupPort(t)
	// Run the process in a subtest so cleanup kills it before the restart.
	var mailbox registration
	t.Run("first launch", func(t *testing.T) {
		output := startStartupProcess(t, root, port)
		if strings.Count(output, "WARNING:") != 1 || strings.Index(output, "WARNING:") > strings.Index(output, "listening on") {
			t.Fatalf("startup warning ordering = %q", output)
		}
		assertDirectoryEntries(t, root, 0)
		client := &http.Client{Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		response, err := client.Post(base+apiPrefix+"/registrations", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&mailbox)
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil {
			t.Fatalf("registration = %d, %v, %v", response.StatusCode, decodeErr, closeErr)
		}
		response, err = client.Post(base+messagePath(mailbox.ID), "application/octet-stream", strings.NewReader("live payload"))
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("delivery = %d", response.StatusCode)
		}

	})
	assertDirectoryEntries(t, mailbox.Directory, 1)
	t.Run("restart after killed owner", func(t *testing.T) {
		output := startStartupProcess(t, root, port)
		if !strings.Contains(output, "WARNING:") {
			t.Fatalf("restart did not warn: %q", output)
		}
		assertDirectoryEntries(t, root, 0)
		client := &http.Client{Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		response, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d", port)+messagePath(mailbox.ID), "application/octet-stream", strings.NewReader("stale"))
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("old registration after restart = %d", response.StatusCode)
		}
	})
	t.Run("empty launch", func(t *testing.T) {
		output := startStartupProcess(t, root, port)
		if strings.Contains(output, "WARNING:") {
			t.Fatalf("empty startup warned: %q", output)
		}
		assertDirectoryEntries(t, root, 0)
	})
}

func TestRunRejectsFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(os.PathSeparator)
	err := run(context.Background(), config{port: unusedStartupPort(t), mailboxRoot: root}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "dedicated directory") {
		t.Fatalf("filesystem root accepted: %v", err)
	}
}

func TestStartupCleanupDoesNotFollowSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	protected := filepath.Join(outside, "protected.dat")
	if err := os.WriteFile(protected, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := clearMailboxRoot(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertDirectoryEntries(t, root, 0)
	data, err := os.ReadFile(protected)
	if err != nil || string(data) != "keep" {
		t.Fatalf("cleanup changed external content: %q, %v", data, err)
	}
}

func TestRunCleanupFailureReleasesReservedPort(t *testing.T) {
	root := t.TempDir()
	release := preventStartupRemoval(t, root)
	port := unusedStartupPort(t)
	var output bytes.Buffer
	err := run(context.Background(), config{port: port, mailboxRoot: root}, &output)
	if err == nil || !strings.Contains(err.Error(), "startup cleanup") || strings.Contains(output.String(), "listening on") {
		t.Fatalf("failed cleanup startup = %v, %q", err, output.String())
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("failed cleanup did not release its reserved port: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	release()
	// Correcting the filesystem failure permits the next ordinary run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, config{port: port, mailboxRoot: root}, io.Discard); err != nil {
		t.Fatalf("recovery from cleanup failure: %v", err)
	}
	assertDirectoryEntries(t, root, 0)
}

func preventStartupRemoval(t *testing.T, root string) func() {
	t.Helper()
	file := filepath.Join(root, "protected.dat")
	if err := os.WriteFile(file, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	var restore func()
	if runtime.GOOS == "windows" {
		current, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		sid := "*" + current.Uid
		for _, denial := range []struct{ path, rights string }{{file, "D"}, {root, "DC"}} {
			if err := exec.Command("icacls", denial.path, "/deny", sid+":("+denial.rights+")").Run(); err != nil {
				t.Fatalf("deny startup removal: %v", err)
			}
			path := denial.path
			t.Cleanup(func() {
				if _, err := os.Stat(path); os.IsNotExist(err) {
					return
				}
				if err := exec.Command("icacls", path, "/remove:d", sid).Run(); err != nil {
					t.Errorf("restore temporary directory ACL: %v", err)
				}
			})
		}
		restore = func() {
			for _, path := range []string{file, root} {
				if err := exec.Command("icacls", path, "/remove:d", sid).Run(); err != nil {
					t.Errorf("restore startup removal: %v", err)
				}
			}
		}
	} else {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory removal permissions")
		}
		if err := os.Chmod(root, 0500); err != nil {
			t.Fatal(err)
		}
		restore = func() {
			if err := os.Chmod(root, 0700); err != nil {
				t.Error(err)
			}
		}
	}
	var once sync.Once
	release := func() { once.Do(restore) }
	t.Cleanup(release)
	return release
}

func TestStartupCleanupDoesNotFollowJunctions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows-specific")
	}
	root, outside := t.TempDir(), t.TempDir()
	protected := filepath.Join(outside, "protected.dat")
	if err := os.WriteFile(protected, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "outside")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Fatalf("create temporary junction: %v, %s", err, output)
	}
	if err := clearMailboxRoot(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertDirectoryEntries(t, root, 0)
	data, err := os.ReadFile(protected)
	if err != nil || string(data) != "keep" {
		t.Fatalf("cleanup changed external content: %q, %v", data, err)
	}
}

func TestRunOrdinaryLaunchCleansEveryEntry(t *testing.T) {
	root := t.TempDir()
	for i := range 300 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("old-%03d.dat", i)), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, config{port: unusedStartupPort(t), mailboxRoot: root}, &output); err != nil {
		t.Fatal(err)
	}
	assertDirectoryEntries(t, root, 0)
	if strings.Count(output.String(), "WARNING:") != 1 {
		t.Fatalf("cleanup warning count: %q", output.String())
	}
}
