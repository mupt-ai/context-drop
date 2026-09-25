package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/api"
	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/storage"
)

var exit = os.Exit

func main() {
	if wantsHelp(os.Args[1:]) {
		fmt.Fprint(os.Stdout, serverHelp)
		return
	}
	if err := run(); err != nil {
		slog.Error("server failed", "err", err)
		exit(1)
	}
}

const serverHelp = `Run the Context Drop HTTP server.

Usage:
  context-drop-server

Configuration is provided with environment variables:
  CONTEXT_DROP_ADDR                  listen address (default :8080)
  CONTEXT_DROP_BASE_URL              public base URL for generated drop links
  CONTEXT_DROP_STORAGE               storage backend: local or gcs (default local)
  CONTEXT_DROP_DATA_DIR              data directory for local storage (default .data)
  CONTEXT_DROP_GCS_BUCKET            GCS bucket for gcs storage
  CONTEXT_DROP_GCS_PREFIX            optional GCS object prefix
  CONTEXT_DROP_UPLOAD_TOKEN          required bearer token for uploads
  CONTEXT_DROP_DEFAULT_TTL           default drop TTL (default 24h)
  CONTEXT_DROP_MAX_TTL               maximum drop TTL (default 168h)
  CONTEXT_DROP_MAX_BYTES             maximum upload size in bytes (default 26214400)
`

func wantsHelp(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")
}

func run() error {
	cfg, err := config.LoadServerConfig()
	if err != nil {
		return err
	}
	return runWithConfig(context.Background(), cfg)
}

func runWithConfig(ctx context.Context, cfg config.ServerConfig) error {
	store, err := newStore(ctx, cfg)
	if err != nil {
		return err
	}
	httpServer := newHTTPServer(cfg, store)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, httpServer, cfg)
}

func newHTTPServer(cfg config.ServerConfig, store storage.Store) *http.Server {
	apiServer := api.NewServer(api.Options{
		BaseURL:     cfg.BaseURL,
		Store:       store,
		UploadToken: cfg.UploadToken,
		DefaultTTL:  cfg.DefaultTTL,
		MaxTTL:      cfg.MaxTTL,
		MaxBytes:    cfg.MaxBytes,
	})

	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       70 * time.Second,
		WriteTimeout:      70 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func serve(ctx context.Context, httpServer *http.Server, cfg config.ServerConfig) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("context-drop server listening", "addr", cfg.Addr, "storage", cfg.Storage, "base_url", cfg.BaseURL)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func newStore(ctx context.Context, cfg config.ServerConfig) (storage.Store, error) {
	switch cfg.Storage {
	case "", "local":
		return storage.NewLocal(cfg.DataDir), nil
	case "gcs":
		return storage.NewGCS(ctx, cfg.GCSBucket, cfg.GCSPrefix)
	default:
		return nil, fmt.Errorf("unknown CONTEXT_DROP_STORAGE %q", cfg.Storage)
	}
}
