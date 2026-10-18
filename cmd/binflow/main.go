// Command binflow runs the BinFlow HTTP service.
// binflow 命令启动 BinFlow 的 HTTP 服务。
//
// This is the P0 smoke entry point: it serves the internal /healthz
// endpoint only and is not an Artifactory-compatible service.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0ldlight/bf/internal/httpserver"
)

const (
	defaultListenAddr = "127.0.0.1:8080"
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// listenAddr returns the listen address, honouring BINFLOW_LISTEN_ADDR.
func listenAddr() string {
	if addr := os.Getenv("BINFLOW_LISTEN_ADDR"); addr != "" {
		return addr
	}
	return defaultListenAddr
}

func main() {
	srv := &http.Server{
		Addr:    listenAddr(),
		Handler: httpserver.NewHandler(),
		// Bound so slow or stalled clients cannot hold connections open
		// indefinitely (Slowloris-style resource exhaustion).
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Stop cleanly on SIGINT/SIGTERM; the shutdown below is bounded by
	// shutdownTimeout so a stuck request cannot block exit forever.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("binflow: listening on %s", srv.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("binflow: server error: %v", err)
		}
		return
	case <-ctx.Done():
		log.Print("binflow: shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("binflow: graceful shutdown failed: %v", err)
	}
	log.Print("binflow: stopped")
}
