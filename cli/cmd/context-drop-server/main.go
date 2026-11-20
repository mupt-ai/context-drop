package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/api"
	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/orgserver"
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
  CONTEXT_DROP_UPLOAD_TOKEN          optional bearer token (unset allows public uploads)
  CONTEXT_DROP_DEFAULT_TTL           default drop TTL (default 24h)
  CONTEXT_DROP_MAX_TTL               maximum drop TTL (default 168h)
  CONTEXT_DROP_MAX_BYTES             maximum upload size in bytes (default 26214400)
  CONTEXT_DROP_UPLOADS_PER_HOUR      anonymous uploads per client IP per hour (default 60, 0 = off)
  CONTEXT_DROP_UPLOAD_BYTES_PER_HOUR anonymous upload bytes per client IP per hour (default 262144000, 0 = off)
  CONTEXT_DROP_PROXY_HOPS            trusted proxies in front appending X-Forwarded-For (Cloud Run: 1; GCP HTTPS LB: 2)
  CONTEXT_DROP_ADMIN_TOKEN           bearer token for creating orgs (unset disables org creation)
  CONTEXT_DROP_ORG_STATE             file holding orgs and joined machines (default <data dir>/orgs.json)
  CONTEXT_DROP_DENO                  Deno executable for mods (default: deno on PATH; without it mods are off)
  CONTEXT_DROP_MODS_DIR              deployed mods, their data, and secrets (default <data dir>/mods)
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
	orgs, err := newOrgServer(cfg)
	if err != nil {
		return err
	}
	httpServer := newHTTPServer(cfg, store, orgs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startCleanup(ctx, store, cfg)

	return serve(ctx, httpServer, cfg)
}

func newOrgServer(cfg config.ServerConfig) (*orgserver.Server, error) {
	path := cfg.OrgState
	if path == "" {
		path = filepath.Join(cfg.DataDir, "orgs.json")
	}
	st, err := orgserver.OpenStore(path)
	if err != nil {
		return nil, err
	}
	s := orgserver.New(st, cfg.AdminToken, nil)
	if rt, err := mods.NewRuntime(cfg.Deno); err != nil {
		slog.Warn("mods are off", "err", err)
	} else {
		dir := cfg.ModsDir
		if dir == "" {
			dir = filepath.Join(cfg.DataDir, "mods")
		}
		ms, err := mods.OpenStore(dir)
		if err != nil {
			return nil, err
		}
		s.Mods = mods.NewService(ms, rt, loopback(cfg.Addr), nil)
	}
	return s, nil
}

// loopback is the URL mods use to call this server from the same host.
func loopback(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

func newHTTPServer(cfg config.ServerConfig, store storage.Store, orgs *orgserver.Server) *http.Server {
	apiServer := api.NewServer(api.Options{
		BaseURL:     cfg.BaseURL,
		Store:       store,
		UploadToken: cfg.UploadToken,
		DefaultTTL:  cfg.DefaultTTL,
		MaxTTL:      cfg.MaxTTL,
		MaxBytes:    cfg.MaxBytes,

		UploadsPerHour:     cfg.UploadsPerHour,
		UploadBytesPerHour: cfg.UploadBytesPerHour,
		ProxyHops:          cfg.ProxyHops,
		Orgs:               orgs,
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

// startCleanup deletes expired drops. Local storage is swept hourly; on GCS the
// bucket's lifecycle rule deletes objects, so the server only checks one exists.
func startCleanup(ctx context.Context, store storage.Store, cfg config.ServerConfig) {
	switch s := store.(type) {
	case *storage.LocalStore:
		go func() {
			sweep := func() {
				if n, err := s.DeleteExpired(ctx, time.Now()); err != nil {
					slog.Warn("delete expired drops", "err", err)
				} else if n > 0 {
					slog.Info("deleted expired drops", "count", n)
				}
			}
			sweep()
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					sweep()
				}
			}
		}()
	case *storage.GCSStore:
		go func() {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			ok, err := s.HasDeleteLifecycle(ctx, cfg.MaxTTL)
			switch {
			case err != nil:
				slog.Warn("could not read the bucket lifecycle; make sure a delete rule removes expired drops", "bucket", cfg.GCSBucket, "err", err)
			case !ok:
				slog.Warn("bucket has no lifecycle rule deleting objects after the max TTL; expired drops will never be deleted. See docs/server.md", "bucket", cfg.GCSBucket, "max_ttl", cfg.MaxTTL)
			}
		}()
	}
}
