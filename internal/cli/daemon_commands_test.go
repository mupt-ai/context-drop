package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/orchestrator"
)

func TestScheduleSilencePreservesDefinition(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	store, err := orchestrator.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *orchestrator.State) error {
		st.Schedules = []orchestrator.Schedule{{Name: "backend", Prompt: "keep this", Enabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--silent", "--silent=false"} {
		root := NewRootCommand(BuildInfo{})
		root.SetOut(&bytes.Buffer{})
		root.SetArgs([]string{"schedule", "backend", flag})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		state, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		s := state.Schedules[0]
		if s.Silent != (flag == "--silent") || s.Prompt != "keep this" || !s.Enabled {
			t.Fatalf("unexpected schedule: %+v", s)
		}
	}
}

func TestDaemonAndScheduleCommandsRegistered(t *testing.T) {
	root := NewRootCommand(BuildInfo{})
	for _, path := range [][]string{{"daemon", "status"}, {"daemon", "install"}, {"daemon", "watchdog", "status"}, {"schedule", "add"}, {"schedule", "run"}, {"schedule", "run-now"}, {"schedule", "pause"}, {"schedule", "resume"}} {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			t.Fatalf("command %v not registered: %v", path, err)
		}
	}
}

func TestSchedulePositionalPromptShowAndSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	runtimeDir := filepath.Join(home, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// This command validates the executable path but never launches Node.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"host": "127.0.0.1", "port": 1, "stateDir": home,
		"tokenFile": filepath.Join(home, "token"), "nodePath": executable,
		"herdrSession": "default", "agents": map[string]any{"pi": map[string]any{"command": []string{"pi"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "config.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCommand(BuildInfo{})
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)

	add := []string{"schedule", "add", "--name", "demo", "--repo", "/tmp", "--prompt", "original prompt", "--every", "1h"}
	root.SetArgs(add)
	if err := root.Execute(); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Show the prompt with the positional name form.
	root = NewRootCommand(BuildInfo{})
	out = &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"schedule", "demo"})
	if err := root.Execute(); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out.String(), "original prompt") {
		t.Fatalf("show output missing prompt: %q", out.String())
	}

	// Update the prompt with the positional form.
	root = NewRootCommand(BuildInfo{})
	out = &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"schedule", "demo", "revised prompt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("set: %v", err)
	}

	root = NewRootCommand(BuildInfo{})
	out = &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"schedule", "demo"})
	if err := root.Execute(); err != nil {
		t.Fatalf("show after set: %v", err)
	}
	if !strings.Contains(out.String(), "revised prompt") || strings.Contains(out.String(), "original prompt") {
		t.Fatalf("prompt not updated: %q", out.String())
	}

	// The other fields survive the edit.
	listOut := &bytes.Buffer{}
	root = NewRootCommand(BuildInfo{})
	root.SetOut(listOut)
	root.SetArgs([]string{"schedule", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listOut.String(), "agent=pool") || !strings.Contains(listOut.String(), "1h") {
		t.Fatalf("other fields not preserved: %q", listOut.String())
	}
}

func TestSchedulePositionalPromptNotFound(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	root := NewRootCommand(BuildInfo{})
	root.SetArgs([]string{"schedule", "missing", "new prompt"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestScheduleSubcommandsStillRoute(t *testing.T) {
	root := NewRootCommand(BuildInfo{})
	if cmd, _, err := root.Find([]string{"schedule", "list"}); err != nil || cmd.Name() != "list" {
		t.Fatalf("list did not route: %v %v", cmd.Name(), err)
	}
}
