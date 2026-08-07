package daemon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/runtimeclient"
)

func TestRenderedLaunchAgentHasExplicitHomebrewNodePath(t *testing.T) {
	plist := RenderLaunchAgent("/tmp/context-drop", "/tmp/daemon.log", "/opt/homebrew/bin/node", false)
	if !strings.Contains(plist, "/opt/homebrew/bin") || !strings.Contains(plist, "<key>EnvironmentVariables</key>") {
		t.Fatalf("launch agent did not carry a minimal-PATH safe node environment: %s", plist)
	}
}

func TestResolveExecutableProducesAbsolutePathWithMinimalPATH(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin))
	got, err := runtimeclient.ResolveExecutable("node")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !filepath.IsAbs(got) {
		t.Fatalf("got %q, want canonical absolute %q", got, want)
	}
}

func TestRuntimePortConflictIsExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	node := filepath.Join(home, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, configPath, tokenPath, err := runtimeclient.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeclient.RuntimeConfig{Host: "127.0.0.1", Port: port, StateDir: dir, TokenFile: tokenPath, NodePath: node, TmuxSession: "context-drop", Agents: map[string]runtimeclient.AgentConfig{}}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtimePortConflict(); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestActiveDaemonRejectsMismatchedPIDIdentityWithoutSignaling(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process identity is platform specific")
	}
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	dir, _, _, err := Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "process.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	// A real held flock makes activeDaemon follow the PID verification path.
	if err := lockFileForTest(lock); err != nil {
		t.Skipf("cannot acquire test flock: %v", err)
	}
	identity, err := inspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := writePID(PIDInfo{PID: os.Getpid(), Executable: "/not-context-drop", StartToken: identity.StartToken}); err != nil {
		t.Fatal(err)
	}
	called := false
	original := signalPID
	signalPID = func(int, os.Signal) error { called = true; return nil }
	defer func() { signalPID = original }()
	if err := Stop(); err == nil {
		t.Fatal("expected mismatched identity to be rejected")
	}
	if called {
		t.Fatal("Stop attempted to signal a mismatched PID")
	}
}
