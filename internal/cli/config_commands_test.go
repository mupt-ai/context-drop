package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/runtimeclient"
)

func TestConfigWorkerAgentShowsAndSetsChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	node, err := runtimeclient.ResolveExecutable("node")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(bin, "node")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "pi"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if _, err := runtimeclient.Initialize(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newConfigCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"worker-agent"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "worker agent: claude\n") || !strings.Contains(out.String(), "pi: "+filepath.Join(bin, "pi")+" --approve") {
		t.Fatalf("output = %q", out.String())
	}
	out.Reset()
	cmd = newConfigCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"worker-agent", "pi"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "daemon restart") {
		t.Fatalf("output = %q", out.String())
	}
	cfg, err := runtimeclient.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerAgent != "pi" {
		t.Fatalf("workerAgent = %q", cfg.WorkerAgent)
	}
	cmd = newConfigCommand()
	cmd.SetArgs([]string{"worker-agent", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}
