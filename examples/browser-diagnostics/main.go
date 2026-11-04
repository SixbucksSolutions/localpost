// Command browser-diagnostics serves the development-only synthetic browser pilot.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"
)

//go:embed index.html pilot.mjs
var assets embed.FS

func address(port int) (string, error) {
	if port < 1024 || port > 65535 {
		return "", errors.New("port must be between 1024 and 65535")
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
}

func handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := ""
		switch r.URL.Path {
		case "/":
			name = "index.html"
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "/pilot.mjs":
			name = "pilot.mjs"
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		default:
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method == http.MethodHead {
			return
		}
		// Best-effort response: a client disconnect cannot be repaired here.
		_, _ = w.Write(data)
	})
}

func run(ctx context.Context, port int) error {
	addr, err := address(port)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// Startup output is informational; a closed output stream does not stop serving.
	_, _ = fmt.Printf("Browser diagnostic pilot: http://%s/\n", addr)
	return serve(ctx, listener)
}

func serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler: handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			// Force-close only this pilot's server after its grace period.
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", shutdownErr)
		}
		err = <-done
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	port := flag.Int("port", 53148, "loopback pilot port (1024-65535)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, *port)
	stop()
	if err != nil {
		// Preserve the failure exit status even if stderr itself is unavailable.
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
