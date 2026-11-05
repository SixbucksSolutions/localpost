package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestArchivesDeterministicAndNormalized(t *testing.T) {
	entries := []entry{{"README.md", []byte("read me"), 0644}, {"localpost", []byte("dummy binary"), 0755}, {"licenses/go/LICENSE", []byte("license"), 0644}}
	for _, windows := range []bool{true, false} {
		first, err := archiveBytes("localpost_0.2.0_test_amd64", entries, windows)
		if err != nil {
			t.Fatal(err)
		}
		reversed := []entry{entries[2], entries[1], entries[0]}
		second, err := archiveBytes("localpost_0.2.0_test_amd64", reversed, windows)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Fatal("input ordering changed archive bytes")
		}
		got := make([]entry, 0, len(entries))
		if windows {
			reader, readErr := zip.NewReader(bytes.NewReader(first), int64(len(first)))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, file := range reader.File {
				if !file.Modified.Equal(archiveTime) {
					t.Fatalf("unexpected ZIP time: %v", file.Modified)
				}
				content, openErr := file.Open()
				if openErr != nil {
					t.Fatal(openErr)
				}
				data, dataErr := io.ReadAll(content)
				closeErr := content.Close()
				if err = errors.Join(dataErr, closeErr); err != nil {
					t.Fatal(err)
				}
				got = append(got, entry{strings.TrimPrefix(file.Name, "localpost_0.2.0_test_amd64/"), data, int64(file.Mode().Perm())})
			}
		} else {
			reader, readErr := gzip.NewReader(bytes.NewReader(first))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !reader.ModTime.Equal(archiveTime) || reader.Name != "" || reader.Comment != "" {
				t.Fatal("unexpected gzip metadata")
			}
			archive := tar.NewReader(reader)
			for {
				header, nextErr := archive.Next()
				if errors.Is(nextErr, io.EOF) {
					break
				}
				if nextErr != nil {
					t.Fatal(nextErr)
				}
				if !header.ModTime.Equal(archiveTime) || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
					t.Fatal("unexpected tar metadata")
				}
				data, dataErr := io.ReadAll(archive)
				if dataErr != nil {
					t.Fatal(dataErr)
				}
				got = append(got, entry{strings.TrimPrefix(header.Name, "localpost_0.2.0_test_amd64/"), data, header.Mode})
			}
			if err = reader.Close(); err != nil {
				t.Fatal(err)
			}
		}
		want := []entry{entries[0], entries[2], entries[1]}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("archive content mismatch: %v", got)
		}
	}
}

func TestPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, version, status, commit string
		commandErr                    bool
		wantError                     bool
	}{
		{name: "clean", version: "0.2.0", commit: strings.Repeat("a", 40)},
		{name: "unsafe", version: "../../bad", wantError: true},
		{name: "leading zero", version: "01.1.0", wantError: true},
		{name: "dirty", version: "0.2.0", status: " M file", wantError: true},
		{name: "untracked", version: "0.2.0", status: "?? file", wantError: true},
		{name: "unidentified", version: "0.2.0", commit: "abc", wantError: true},
		{name: "git failure", version: "0.2.0", commandErr: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(_ string, _ []string, name string, args ...string) ([]byte, error) {
				if name != "git" {
					t.Fatalf("unexpected command %s", name)
				}
				if tc.commandErr {
					return nil, errors.New("test Git failure")
				}
				if args[2] == "status" {
					return []byte(tc.status), nil
				}
				return []byte(tc.commit + "\n"), nil
			}
			_, err := preflight(run, "checkout", tc.version)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}

func TestOutputGuard(t *testing.T) {
	dir := t.TempDir()
	if err := emptyOutput(filepath.Join(dir, "new")); err != nil {
		t.Fatal(err)
	}
	if err := emptyOutput(dir); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "keep")
	if err := os.WriteFile(filename, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := emptyOutput(dir); err == nil {
		t.Fatal("accepted nonempty directory")
	}
	if err := emptyOutput(filename); err == nil {
		t.Fatal("accepted regular file")
	}
	if err := writeExclusive(filename, []byte("replace")); err == nil {
		t.Fatal("overwrote existing file")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatal("existing content changed")
	}
}

func TestSourceArchiveRejectsEscapesAndLinks(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}, {Name: "/absolute", Typeflag: tar.TypeReg}, {Name: "safe", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"}, {Name: "folder\\escape", Typeflag: tar.TypeReg}} {
		var buffer bytes.Buffer
		writer := tar.NewWriter(&buffer)
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractSource(buffer.Bytes(), t.TempDir()); err == nil {
			t.Fatalf("accepted unsafe entry %s", header.Name)
		}
	}
}

func TestBuildEnvironment(t *testing.T) {
	for _, key := range []string{"GOFLAGS", "GOEXPERIMENT", "GOWORK", "GOROOT", "GOOS", "GOARCH", "CGO_ENABLED", "GOAMD64", "GOTOOLCHAIN"} {
		t.Setenv(key, "inherited-bad-setting")
	}
	values := make(map[string]string)
	for _, setting := range buildEnv("linux") {
		key, value, _ := strings.Cut(setting, "=")
		values[key] = value
	}
	for key, want := range map[string]string{"GOFLAGS": "", "GOEXPERIMENT": "", "GOWORK": "off", "GOROOT": "", "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOAMD64": "v1", "GOTOOLCHAIN": toolchain} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}
}

func TestGoNoticesIncludeDependencyAncestors(t *testing.T) {
	root := t.TempDir()
	dependency := filepath.Join(root, "src", "vendor", "golang.org", "x", "net", "http2", "hpack")
	if err := os.MkdirAll(dependency, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LICENSE", "PATENTS", "src/vendor/golang.org/x/net/LICENSE", "src/vendor/golang.org/x/net/PATENTS"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(_ string, _ []string, _ string, _ ...string) ([]byte, error) {
		return []byte(dependency + "\n" + t.TempDir() + "\n"), nil
	}
	entries, err := goNotices(run, "unused", root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, item := range entries {
		got[item.name] = true
		if item.mode != 0644 {
			t.Fatal("unexpected notice mode")
		}
	}
	for _, name := range []string{"licenses/go/LICENSE", "licenses/go/PATENTS", "licenses/go/src/vendor/golang.org/x/net/LICENSE", "licenses/go/src/vendor/golang.org/x/net/PATENTS"} {
		if !got[name] {
			t.Errorf("missing notice %s", name)
		}
	}
	if len(got) != 4 {
		t.Fatalf("unexpected notice count %d", len(got))
	}
}

func TestSourceArchiveGlobalPAX(t *testing.T) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": strings.Repeat("a", 40)}}); err != nil {
		t.Fatal(err)
	}
	data := []byte("source bytes")
	if err := writer.WriteHeader(&tar.Header{Name: "nested/source.go", Typeflag: tar.TypeReg, Size: int64(len(data)), Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := extractSource(buffer.Bytes(), dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "nested", "source.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("source bytes differ")
	}
	if _, err = os.Stat(filepath.Join(dir, "pax_global_header")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("PAX metadata became a file")
	}
}

func TestActualGitArchiveExtraction(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	data, err := git(runCommand, root, "archive", "--format=tar", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = extractSource(data, dir); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseTargets(t *testing.T) {
	for _, value := range []string{"windows/amd64,linux/amd64,linux/arm64", "linux/arm64"} {
		got, err := parseTargets(value)
		if err != nil || strings.Join(got, ",") != value {
			t.Fatalf("targets %q: %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "linux", "linux/arm64,linux/arm64", "windows/arm64", "linux/amd64,"} {
		if _, err := parseTargets(value); err == nil {
			t.Fatalf("accepted invalid targets %q", value)
		}
	}
}

func TestARM64BuildEnvironment(t *testing.T) {
	t.Setenv("GOARM64", "v9.5")
	t.Setenv("CGO_ENABLED", "1")
	values := make(map[string]string)
	for _, setting := range buildEnv("linux/arm64") {
		key, value, _ := strings.Cut(setting, "=")
		values[key] = value
	}
	for key, want := range map[string]string{"GOOS": "linux", "GOARCH": "arm64", "GOARM64": "v8.0", "CGO_ENABLED": "0"} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}
}

func TestResolveSource(t *testing.T) {
	commit := strings.Repeat("b", 40)
	run := func(_ string, _ []string, name string, args ...string) ([]byte, error) {
		want := []string{"-c", "safe.directory=checkout", "rev-parse", "--verify", "--end-of-options", "v0.2.0^{commit}"}
		if name != "git" || !reflect.DeepEqual(args, want) {
			t.Fatalf("unexpected source resolution %s %v", name, args)
		}
		return []byte(commit + "\n"), nil
	}
	got, err := resolveSource(run, "checkout", "v0.2.0")
	if err != nil || got != commit {
		t.Fatalf("resolved source %q, %v", got, err)
	}
	if _, err = resolveSource(run, "checkout", ""); err == nil {
		t.Fatal("accepted empty source revision")
	}
	bad := func(_ string, _ []string, _ string, _ ...string) ([]byte, error) {
		return []byte("short"), nil
	}
	if _, err = resolveSource(bad, "checkout", "tag"); err == nil {
		t.Fatal("accepted unidentified source revision")
	}
}

func TestRuntimeReadmeUsesMailboxRootAndCurrentAPI(t *testing.T) {
	for _, required := range []string{"--mailbox-root", "JSON {}", "/api/2026-11-05/registrations", "returned id and directory"} {
		if !strings.Contains(runtimeReadme, required) {
			t.Errorf("runtime README missing %q", required)
		}
	}
	if strings.Contains(runtimeReadme, `{"directory"`) {
		t.Fatal("runtime README retained caller-selected registration directory")
	}
}
