package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"testing"
)

func TestVersionDevelopmentIdentity(t *testing.T) {
	if releaseVersion != "dev" || sourceCommit != "unknown" {
		t.Fatalf("development identity = %q, %q; want dev, unknown", releaseVersion, sourceCommit)
	}
	assertVersionOutput(t, "dev", "unknown")
}

func TestVersionReleaseIdentity(t *testing.T) {
	previousVersion, previousCommit := releaseVersion, sourceCommit
	t.Cleanup(func() { releaseVersion, sourceCommit = previousVersion, previousCommit })
	releaseVersion = "0.1.0"
	sourceCommit = "0123456789abcdef0123456789abcdef01234567"
	assertVersionOutput(t, releaseVersion, sourceCommit)
}

func assertVersionOutput(t *testing.T, version, commit string) {
	t.Helper()
	var output bytes.Buffer
	if err := writeVersion(&output); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("localpost %s (commit %s; %s; %s/%s)\n",
		version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if output.String() != want {
		t.Fatalf("version output = %q; want %q", output.String(), want)
	}
}

func TestVersionExitsWithOccupiedPort(t *testing.T) {
	listener := newTestListener(t)
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener did not provide a TCP address")
	}
	cfg, err := parseConfig([]string{"--version", "--port", strconv.Itoa(address.Port)}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.showVersion {
		t.Fatal("--version did not select version output")
	}
	var output bytes.Buffer
	if err := run(context.Background(), cfg, &output); err != nil {
		t.Fatalf("version attempted to start daemon: %v", err)
	}
	if output.Len() == 0 {
		t.Fatal("version output is empty")
	}
}

func TestVersionRejectsInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "--unknown"},
		{"--version", "--port", "1023"},
		{"--version", "extra"},
		{"--version=invalid"},
	} {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("parseConfig(%q) accepted invalid input", args)
		}
	}
}
