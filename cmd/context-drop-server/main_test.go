package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/pairing"
	"contextdrop.dev/context-drop/internal/storage"
)

type serverExitPanic int

func TestWantsHelp(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{args: []string{"--help"}, want: true},
		{args: []string{"-h"}, want: true},
		{args: []string{"help"}, want: true},
		{args: nil, want: false},
		{args: []string{"--help", "extra"}, want: false},
	} {
		if got := wantsHelp(tt.args); got != tt.want {
			t.Fatalf("wantsHelp(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestServerHelpDocumentsConfig(t *testing.T) {
	if strings.Contains(serverHelp, "SUPABASE") || !strings.Contains(serverHelp, "CONTEXT_DROP_STORAGE") || !strings.Contains(serverHelp, "CONTEXT_DROP_JOIN_TOKEN_TTL") {
		t.Fatalf("serverHelp = %q", serverHelp)
	}
}

func TestMainPrintsHelp(t *testing.T) {
	oldArgs := os.Args
	oldExit := exit
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"context-drop-server", "--help"}
	exit = func(code int) { t.Fatalf("exit(%d) called while printing help", code) }
	os.Stdout = writer
	defer func() {
		os.Args = oldArgs
		exit = oldExit
		os.Stdout = oldStdout
		_ = reader.Close()
	}()

	main()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "Usage:\n  context-drop-server") || strings.Contains(text, "SUPABASE") {
		t.Fatalf("help output = %q", text)
	}
}

func TestRunReturnsConfigError(t *testing.T) {
	t.Setenv("CONTEXT_DROP_DEFAULT_TTL", "bad")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "parse CONTEXT_DROP_DEFAULT_TTL") {
		t.Fatalf("run() error = %v, want config parse error", err)
	}
}

func TestMainErrorExits(t *testing.T) {
	oldExit := exit
	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	exit = func(code int) { panic(serverExitPanic(code)) }
	os.Stderr = writer
	t.Setenv("CONTEXT_DROP_DEFAULT_TTL", "bad")
	t.Cleanup(func() {
		exit = oldExit
		os.Stderr = oldStderr
		_ = reader.Close()
	})

	defer func() {
		got := recover()
		if got != serverExitPanic(1) {
			t.Fatalf("recover() = %v, want serverExitPanic(1)", got)
		}
		_ = writer.Close()
		_, _ = io.ReadAll(reader)
	}()
	main()
	t.Fatal("main returned without exiting")
}

func TestRunWithConfigReturnsServeError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	cfg := config.ServerConfig{
		Addr:         listener.Addr().String(),
		Storage:      "local",
		DataDir:      t.TempDir(),
		DefaultTTL:   time.Hour,
		JoinTokenTTL: time.Minute,
		MaxTTL:       24 * time.Hour,
		MaxBytes:     1024,
	}
	err = runWithConfig(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("runWithConfig() error = %v, want address already in use", err)
	}
}

func TestRunWithConfigReturnsStoreError(t *testing.T) {
	cfg := config.ServerConfig{Storage: "unknown", DataDir: t.TempDir(), DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: 1024}
	err := runWithConfig(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown CONTEXT_DROP_STORAGE") {
		t.Fatalf("runWithConfig() error = %v, want storage error", err)
	}
}

func TestNewStore(t *testing.T) {
	local, err := newStore(context.Background(), config.ServerConfig{Storage: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local.(*storage.LocalStore); !ok {
		t.Fatalf("newStore(local) = %T, want *storage.LocalStore", local)
	}
	_, err = newStore(context.Background(), config.ServerConfig{Storage: "gcs"})
	if err == nil || !strings.Contains(err.Error(), "gcs bucket is required") {
		t.Fatalf("newStore(gcs missing bucket) error = %v", err)
	}
	_, err = newStore(context.Background(), config.ServerConfig{Storage: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown CONTEXT_DROP_STORAGE") {
		t.Fatalf("newStore(bogus) error = %v", err)
	}
}

func TestNewPairingStore(t *testing.T) {
	local, err := newPairingStore(context.Background(), config.ServerConfig{Storage: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local.(pairing.Store); !ok {
		t.Fatalf("newPairingStore(local) = %T, want pairing.Store", local)
	}
	_, err = newPairingStore(context.Background(), config.ServerConfig{Storage: "gcs"})
	if err == nil || !strings.Contains(err.Error(), "gcs bucket is required") {
		t.Fatalf("newPairingStore(gcs missing bucket) error = %v", err)
	}
	_, err = newPairingStore(context.Background(), config.ServerConfig{Storage: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown CONTEXT_DROP_STORAGE") {
		t.Fatalf("newPairingStore(bogus) error = %v", err)
	}
}

func TestNewHTTPServerConfiguresServer(t *testing.T) {
	cfg := config.ServerConfig{Addr: "127.0.0.1:0", BaseURL: "http://example.test", DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: 1024}
	server := newHTTPServer(cfg, storage.NewLocal(t.TempDir()), pairing.NewMemory())
	if server.Addr != cfg.Addr {
		t.Fatalf("Addr = %q, want %q", server.Addr, cfg.Addr)
	}
	if server.Handler == nil {
		t.Fatal("Handler = nil")
	}
	if server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 70*time.Second || server.WriteTimeout != 70*time.Second || server.IdleTimeout != 120*time.Second {
		t.Fatalf("unexpected timeouts: %+v", server)
	}
}

func TestServeReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &http.Server{Addr: listener.Addr().String(), Handler: http.NewServeMux()}
	err = serve(context.Background(), server, config.ServerConfig{Addr: server.Addr, Storage: "local"})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("serve() error = %v, want address already in use", err)
	}
}

func TestServeShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}

	err := serve(ctx, server, config.ServerConfig{Addr: server.Addr, Storage: "local"})
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve() error = %v, want nil or ErrServerClosed", err)
	}
}
