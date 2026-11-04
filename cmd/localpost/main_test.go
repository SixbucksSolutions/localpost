package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		port int
	}{
		{name: "default", port: 53147},
		{name: "minimum", args: []string{"--port", "1024"}, port: 1024},
		{name: "override", args: []string{"--port=53148"}, port: 53148},
		{name: "maximum", args: []string{"--port", "65535"}, port: 65535},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfig(tc.args, io.Discard)
			if err != nil || cfg.port != tc.port {
				t.Fatalf("parseConfig() = %+v, %v; want port %d", cfg, err, tc.port)
			}
		})
	}
}

func TestParseConfigRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		{"--port", "-1"}, {"--port", "0"}, {"--port", "1023"},
		{"--port", "65536"}, {"--port", "999999999999999999999"},
		{"--port", "no"}, {"--port"}, {"--unknown"},
		{"extra"}, {"--port", "53148", "extra"}, {"--", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			_, err := parseConfig(args, &output)
			if err == nil || err.Error() == "" {
				t.Fatal("invalid input must return an explanatory error")
			}
		})
	}
}

func TestParseConfigHelp(t *testing.T) {
	var output bytes.Buffer
	_, err := parseConfig([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(output.String(), "port") {
		t.Fatalf("help = %v, %q", err, output.String())
	}
}

func TestRunOccupiedPort(t *testing.T) {
	listener := newTestListener(t)
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener did not provide a TCP address")
	}
	port := address.Port
	var output bytes.Buffer
	err := run(context.Background(), config{port: port}, &output)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatalf("occupied port must identify failed address: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed startup announced a listener: %q", output.String())
	}
}

func TestServeHTTPAndCancellation(t *testing.T) {
	listener := newTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- serve(ctx, listener) }()

	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || response.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "404") {
		t.Fatalf("placeholder HTTP response = %d, %q, %v", response.StatusCode, body, err)
	}

	// An incomplete request must not keep the process alive on cancellation.
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Test cleanup can race with the server closing its side of the connection.
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\n"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("cancellation returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var timeout net.Error
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained readable after server shutdown")
	} else if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("connection remained open after server shutdown")
	}
	// Rebinding the exact address proves shutdown released the listener.
	rebound, err := net.Listen("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatalf("listener was not released: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServeAlreadyCancelled(t *testing.T) {
	listener := newTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() { result <- serve(ctx, listener) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("already cancelled server returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("already cancelled server did not stop")
	}
}

func newTestListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Serve may already have closed this listener.
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}
