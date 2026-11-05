// Package main provides the localpost loopback HTTP daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"
)

const defaultPort = 53147

type config struct {
	port        int
	mailboxRoot string
	showVersion bool
}

func parseConfig(args []string, output io.Writer) (config, error) {
	cfg := config{port: defaultPort}
	flags := flag.NewFlagSet("localpost", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.IntVar(&cfg.port, "port", defaultPort, "TCP port (1024-65535)")
	flags.StringVar(&cfg.mailboxRoot, "mailbox-root", "", "Existing absolute mailbox root directory (required)")
	flags.BoolVar(&cfg.showVersion, "version", false, "Print version and exit")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if cfg.port < 1024 || cfg.port > 65535 {
		return config{}, fmt.Errorf("port must be between 1024 and 65535 (got %d)", cfg.port)
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config, output io.Writer) error {
	if cfg.showVersion {
		return writeVersion(output)
	}
	root, err := prepareMailboxRoot(cfg.mailboxRoot)
	if err != nil {
		return fmt.Errorf("mailbox root: %w", err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.port))
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	// Closing an already stopped listener is best-effort cleanup.
	defer func() { _ = listener.Close() }()
	// Reserve the requested port before deleting anything. An occupied port
	// rejects a duplicate launch without touching the running instance's files.
	if err := clearMailboxRoot(root, output); err != nil {
		return fmt.Errorf("mailbox root: %w", err)
	}
	// Losing a diagnostic output stream does not change listener ownership.
	_, _ = fmt.Fprintf(output, "localpost listening on http://%s\n", listener.Addr())
	return serve(ctx, listener, root)
}

func serve(ctx context.Context, listener net.Listener, root string) error {
	return serveWithRegistry(ctx, listener, newRegistry(root))
}

func serveWithRegistry(ctx context.Context, listener net.Listener, registrations *registry) error {
	defer registrations.close()
	connections := &connectionTracker{states: make(map[net.Conn]http.ConnState)}
	server := &http.Server{
		Handler:           withCORS(registrations, "POST, DELETE, OPTIONS"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		ConnState:         connections.track,
	}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(stopped)
		grace, cancel := context.WithTimeout(context.Background(), registrations.gracePeriod)
		defer cancel()
		// Freeze registration admission before closing the single listener. All
		// accepted uploads share this one grace budget.
		mailboxes := registrations.stopAll()
		connections.stopIdle()
		if err := server.Shutdown(grace); err != nil {
			for _, mailbox := range mailboxes {
				select {
				case <-mailbox.done:
				default:
					mailbox.force()
				}
			}
			_ = server.Close()
		}
		registrations.shutdown(grace)
		for _, mailbox := range mailboxes {
			<-mailbox.done
		}
	})
	defer stop()
	defer func() { _ = server.Close() }()
	err := server.Serve(listener)
	if ctx.Err() != nil {
		<-stopped
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

// Incomplete headers have not admitted an upload. Close those connections at
// shutdown along with idle keep-alives, while active handlers retain the grace
// period. Ownership stays synchronized with net/http's state callbacks.
type connectionTracker struct {
	mu       sync.Mutex
	states   map[net.Conn]http.ConnState
	stopping bool
}

func (t *connectionTracker) track(connection net.Conn, state http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if state == http.StateClosed || state == http.StateHijacked {
		delete(t.states, connection)
		return
	}
	t.states[connection] = state
	if t.stopping && (state == http.StateNew || state == http.StateIdle) {
		_ = connection.Close()
	}
}

func (t *connectionTracker) stopIdle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopping = true
	for connection, state := range t.states {
		if state == http.StateNew || state == http.StateIdle {
			_ = connection.Close()
		}
	}
}

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		err = run(ctx, cfg, os.Stdout)
		stop()
	}
	if err != nil {
		// There is no secondary diagnostic channel if stderr is unavailable.
		_, _ = fmt.Fprintf(os.Stderr, "localpost: ERROR: %v\n", err)
		os.Exit(1)
	}
}
