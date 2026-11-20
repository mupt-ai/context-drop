package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude is a claude CLI that prints where it ran and what it was given.
func fakeClaude(t *testing.T, script string) *Env {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Env{Getenv: func(k string) string {
		if k == "CONTEXT_DROP_CLAUDE_BIN" {
			return bin
		}
		return ""
	}}
}

func TestAskAnswersFromAReadOnlyCopyOfTheSession(t *testing.T) {
	env := fakeClaude(t, `pwd; for a in "$@"; do printf '%s\n' "$a"; done`)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	s := Session{ID: "cc-1a2b3c4d", Agent: Claude, NativeID: "6a79295a-0000", Cwd: dir}
	got, err := Ask(context.Background(), env, s, "does parse() set stmt.sql_text?", SendOptions{ReplyTo: "laptop/cc-99999999"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{dir, "--resume\n6a79295a-0000", "--fork-session", "--no-session-persistence",
		"--tools\nRead,Grep,Glob", `{"disableAllHooks":true}`, "--strict-mcp-config", "Agent laptop/cc-99999999 (not your user) asks", "temporary copy", "does parse() set stmt.sql_text?"} {
		if !strings.Contains(got, want) {
			t.Errorf("answer lacks %q:\n%s", want, got)
		}
	}
}

func TestAskRefusesWhatCannotBeCopied(t *testing.T) {
	env := fakeClaude(t, "echo should not run")
	for _, s := range []Session{
		{ID: "cx-1", Agent: Codex, NativeID: "x"},
		{ID: "cc-2", Agent: Claude, NativeID: "p/s", Subagent: true},
	} {
		if _, err := Ask(context.Background(), env, s, "hi", SendOptions{}); err == nil {
			t.Errorf("asked %s", s.ID)
		}
	}
	failing := fakeClaude(t, "echo 'No conversation found' >&2; exit 1")
	if _, err := Ask(context.Background(), failing, Session{ID: "cc-3", Agent: Claude, NativeID: "gone"}, "hi", SendOptions{}); err == nil || !strings.Contains(err.Error(), "No conversation found") {
		t.Fatalf("a failing fork: %v", err)
	}
}
