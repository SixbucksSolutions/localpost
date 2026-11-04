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
	"time"
)

const defaultPort = 53147

type config struct {
	port int
}

func parseConfig(args []string, output io.Writer) (config, error) {
	cfg := config{port: defaultPort}
	flags := flag.NewFlagSet("localpost", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.IntVar(&cfg.port, "port", defaultPort, "admin TCP port (1024-65535)")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if cfg.port < 1024 || cfg.port > 65535 {
		return config{}, fmt.Errorf("admin port must be between 1024 and 65535 (got %d)", cfg.port)
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config, output io.Writer) error {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.port))
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	// Closing an already stopped listener is best-effort cleanup.
	defer func() { _ = listener.Close() }()
	// Losing a diagnostic output stream does not change listener ownership.
	_, _ = fmt.Fprintf(output, "localpost listening on http://%s\n", listener.Addr())
	return serve(ctx, listener)
}

func serve(ctx context.Context, listener net.Listener) error {
	return serveWithRegistry(ctx, listener, newRegistry())
}

func serveWithRegistry(ctx context.Context, listener net.Listener, registrations *registry) error {
	defer registrations.close()
	server := &http.Server{
		Handler:           withCORS(registrations, "POST, DELETE, OPTIONS"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(stopped)
		grace, cancel := context.WithTimeout(context.Background(), registrations.gracePeriod)
		defer cancel()
		// Freeze every mailbox before closing admin connections. Existing uploads
		// use their own connections and continue within the common grace budget.
		mailboxes := registrations.stopAll()
		_ = listener.Close()
		_ = server.Close()
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
		return fmt.Errorf("serve admin HTTP: %w", err)
	}
	return nil
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
		_, _ = fmt.Fprintf(os.Stderr, "localpost: %v\n", err)
		os.Exit(1)
	}
}
