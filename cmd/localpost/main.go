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
	defer listener.Close()
	fmt.Fprintf(output, "localpost listening on http://%s\n", listener.Addr())
	return serve(ctx, listener)
}

func serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           http.NotFoundHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Only this admin listener exists so far. Full mailbox draining belongs to
	// the later lifecycle implementation; cancellation releases all connections.
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	defer server.Close()
	if err := server.Serve(listener); err != nil {
		if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
			return nil
		}
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
		defer stop()
		err = run(ctx, cfg, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "localpost: %v\n", err)
		os.Exit(1)
	}
}
