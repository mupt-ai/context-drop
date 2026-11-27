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
	"strings"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/api"
	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/extensions/linear"
	"contextdrop.dev/context-drop/internal/extensions/slack"
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
  CONTEXT_DROP_ORG_STATE             SQLite database of orgs, machines, and extensions (default <data dir>/orgs.db; a .json path is imported once). On GCS storage it is backed up to state/ in the bucket
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
	orgs, err := newOrgServer(ctx, cfg, store)
	if err != nil {
		return err
	}
	httpServer := newHTTPServer(cfg, store, orgs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startCleanup(ctx, store, cfg)

	return serve(ctx, httpServer, cfg)
}

// stateBackup is where the org database is copied, so a lost disk loses at most a few minutes.
type stateBackup interface {
	PutState(ctx context.Context, name, path string) error
	GetState(ctx context.Context, name, path string) (bool, error)
}

// backupEvery is how often the org database is copied to the bucket, when it changed.
const backupEvery = 5 * time.Minute

func newOrgServer(ctx context.Context, cfg config.ServerConfig, drops storage.Store) (*orgserver.Server, error) {
	path := cfg.OrgState
	if path == "" {
		path = filepath.Join(cfg.DataDir, "orgs.db")
	}
	backup, _ := drops.(stateBackup)
	if backup != nil {
		if err := restoreState(ctx, backup, path); err != nil {
			return nil, fmt.Errorf("restore the org database: %w", err)
		}
	}
	st, err := orgserver.OpenStore(path)
	if err != nil {
		return nil, err
	}
	if backup != nil {
		go backUpState(ctx, backup, st)
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
	s.PublicURL = cfg.BaseURL
	if cfg.TokenKey == "" {
		if cfg.SlackClientID != "" || cfg.LinearClientID != "" {
			return nil, fmt.Errorf("the Slack and Linear apps need CONTEXT_DROP_TOKEN_KEY to store tokens")
		}
		slog.Info("slack and linear are off (set CONTEXT_DROP_TOKEN_KEY to turn them on)")
		return s, nil
	}
	st.SetSealKey(cfg.TokenKey)
	if cfg.LinearClientID != "" && (cfg.LinearClientSecret == "" || cfg.LinearWebhookSecret == "") {
		return nil, fmt.Errorf("a Linear app needs CONTEXT_DROP_LINEAR_CLIENT_SECRET and CONTEXT_DROP_LINEAR_WEBHOOK_SECRET with CONTEXT_DROP_LINEAR_CLIENT_ID")
	}
	s.Extensions = append(s.Extensions, &linear.Ext{ClientID: cfg.LinearClientID, ClientSecret: cfg.LinearClientSecret, WebhookSecret: cfg.LinearWebhookSecret})
	switch {
	case cfg.SlackClientID == "":
		slog.Info("slack is off (set CONTEXT_DROP_SLACK_CLIENT_ID to turn it on)")
	case cfg.SlackClientSecret == "" || cfg.SlackSigningSecret == "":
		return nil, fmt.Errorf("slack needs CONTEXT_DROP_SLACK_CLIENT_SECRET and CONTEXT_DROP_SLACK_SIGNING_SECRET with CONTEXT_DROP_SLACK_CLIENT_ID")
	default:
		s.Extensions = append(s.Extensions, &slack.Ext{ClientID: cfg.SlackClientID, ClientSecret: cfg.SlackClientSecret, SigningSecret: cfg.SlackSigningSecret})
	}
	return s, nil
}

// restoreState downloads the last backup when the server has no org state on disk, as on a new VM.
func restoreState(ctx context.Context, b stateBackup, path string) error {
	base := strings.TrimSuffix(strings.TrimSuffix(path, ".json"), ".db")
	db := base + ".db"
	for _, p := range []string{db, base + ".json"} {
		if _, err := os.Stat(p); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		return err
	}
	ok, err := b.GetState(ctx, "orgs.db", db)
	if ok {
		slog.Info("restored the org database from its backup", "path", db)
	}
	return err
}

// backUpState copies the org database to the bucket every few minutes, when it changed, and keeps
// one copy per day. It uploads at least daily even when nothing changed, since the bucket's lifecycle
// rule deletes objects after a few days.
func backUpState(ctx context.Context, b stateBackup, st *orgserver.Store) {
	tmp := st.Path() + ".backup"
	var last time.Time
	t := time.NewTicker(backupEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		wrote, err := st.Snapshot(tmp, time.Since(last) > 24*time.Hour)
		if err == nil && wrote {
			err = b.PutState(ctx, "orgs.db", tmp)
			if err == nil {
				err = b.PutState(ctx, "daily/orgs-"+time.Now().UTC().Format("2006-01-02")+".db", tmp)
			}
			if err == nil {
				last = time.Now()
			} else {
				st.Unsnapshotted() // try again next tick
			}
		}
		if err != nil {
			slog.Warn("back up the org database", "err", err)
		}
		_ = os.Remove(tmp)
	}
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
