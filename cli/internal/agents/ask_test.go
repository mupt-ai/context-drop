package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude is a claude CLI that prints where it ran, its arguments, and its input.
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

type fakeTranscript struct {
	Provider
	msgs []Message
}

func (fakeTranscript) Kind() string { return Codex }
func (f fakeTranscript) Transcript(context.Context, *Env, Session) ([]Message, error) {
	return f.msgs, nil
}

func TestAskAnswersFromTheTranscriptWithReadOnlyTools(t *testing.T) {
	env := fakeClaude(t, `pwd; for a in "$@"; do printf '%s\n' "$a"; done; cat`)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	s := Session{ID: "cx-1a2b3c4d", Agent: Codex, Cwd: dir}
	providers := []Provider{fakeTranscript{msgs: []Message{
		{Role: "user", Text: "make parse() keep the SQL text"},
		{Role: "assistant", Text: "Done: parse() now sets stmt.sql_text."},
	}}}
	got, err := Ask(context.Background(), env, providers, s, "does parse() set stmt.sql_text?", SendOptions{ReplyTo: "laptop/cc-99999999"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{dir, "--tools\nRead,Grep,Glob", `{"disableAllHooks":true}`, "--strict-mcp-config", "--no-session-persistence",
		"Agent laptop/cc-99999999 has a question for agent cx-1a2b3c4d", "[user]\nmake parse() keep the SQL text",
		"[assistant]\nDone: parse() now sets stmt.sql_text.", "Question: does parse() set stmt.sql_text?"} {
		if !strings.Contains(got, want) {
			t.Errorf("answer lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--resume") {
		t.Errorf("Ask resumed the session:\n%s", got)
	}
}

func TestAskKeepsTheEndOfALongTranscript(t *testing.T) {
	old := Message{Role: "user", Text: "OLDEST " + strings.Repeat("x", askBudget)}
	recent := Message{Role: "assistant", Text: "RECENT"}
	got := askPrompt(Session{ID: "cc-1"}, []Message{old, old, recent}, "q", SendOptions{})
	if !strings.Contains(got, "RECENT") || strings.Count(got, "OLDEST") != 1 || !strings.Contains(got, "earlier parts are cut") {
		t.Fatalf("prompt did not keep just the end (%d bytes)", len(got))
	}
}

func TestAskReportsAFailedRun(t *testing.T) {
	providers := []Provider{fakeTranscript{msgs: []Message{{Role: "user", Text: "hi"}}}}
	failing := fakeClaude(t, "echo 'model not found' >&2; exit 1")
	if _, err := Ask(context.Background(), failing, providers, Session{ID: "cx-3", Agent: Codex}, "hi", SendOptions{}); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("a failing run: %v", err)
	}
	empty := []Provider{fakeTranscript{}}
	if _, err := Ask(context.Background(), failing, empty, Session{ID: "cx-4", Agent: Codex}, "hi", SendOptions{}); err == nil {
		t.Fatal("asked about a session with no transcript")
	}
}
