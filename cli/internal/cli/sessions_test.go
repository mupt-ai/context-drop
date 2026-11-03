package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
)

// fakePiHost stages one Pi session without the Context Drop extension, running
// in herdr pane w9N:p2, and records every command the CLI runs.
func fakePiHost(t *testing.T) *[]string {
	t.Helper()
	home := t.TempDir()
	sessionDir := filepath.Join(home, ".pi", "agent", "sessions", "--work-m--")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sessionDir, "2026-11-03T05-00-00-pi-m.jsonl")
	rows := []string{
		`{"type":"session","id":"pi-m-1","cwd":"/work/m"}`,
		`{"type":"message","timestamp":"2026-11-03T05:00:01Z","message":{"role":"user","content":"hello there"}}`,
		`{"type":"message","timestamp":"2026-11-03T05:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"hi!"}]}}`,
	}
	if err := os.WriteFile(file, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var commands []string
	oldEnv, oldProviders := newEnv, allProviders
	t.Cleanup(func() { newEnv, allProviders = oldEnv, oldProviders })
	allProviders = func() []agents.Provider { return agents.Providers() }
	newEnv = func() (*agents.Env, error) {
		return &agents.Env{
			Home: home,
			Run: func(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
				commands = append(commands, strings.Join(append([]string{name}, args...), " "))
				switch {
				case name == "herdr" && args[0] == "pane" && args[1] == "list":
					return []byte(`{"result":{"panes":[{"pane_id":"w9N:p2"}]}}`), nil
				case name == "herdr" && args[0] == "pane" && args[1] == "process-info":
					return []byte(`{"result":{"process_info":{"shell_pid":7,"foreground_processes":[{"pid":42}]}}}`), nil
				case name == "tmux":
					return nil, os.ErrNotExist
				}
				return nil, nil
			},
			Processes: func(context.Context) (map[int]agents.Process, error) {
				return map[int]agents.Process{42: {PID: 42, TTY: "/dev/ttys004", Command: "pi", Start: time.Now().Add(-time.Hour)}}, nil
			},
			OpenFiles: func(context.Context, []string) (map[string][]int, error) { return nil, nil },
			Cwds:      func(context.Context, []int) (map[int]string, error) { return map[int]string{42: "/work/m"}, nil },
			Getenv: func(k string) string {
				if k == "CONTEXT_DROP_HOME" {
					return filepath.Join(home, ".context-drop")
				}
				return ""
			},
		}, nil
	}
	return &commands
}

func TestListReadAndSend(t *testing.T) {
	commands := fakePiHost(t)
	id := agents.MakeID(agents.Pi, "pi-m-1")

	stdout, _, err := executeRoot(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []agents.Session
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || len(listed) != 1 || listed[0].ID != id || listed[0].Cwd != "/work/m" ||
		listed[0].Delivery != "herdr pane w9N:p2" {
		t.Fatalf("list = %s (%v)", stdout, err)
	}

	stdout, _, err = executeRoot(t, "read", id)
	if err != nil || !strings.Contains(stdout, "hello there") || !strings.Contains(stdout, "hi!") {
		t.Fatalf("read = %q, %v", stdout, err)
	}

	_, stderr, err := executeRoot(t, "send", id, "run", "the", "tests")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "sent to "+id+" via herdr pane w9N:p2") {
		t.Fatalf("stderr = %q", stderr)
	}
	joined := strings.Join(*commands, "\n")
	if !strings.Contains(joined, "herdr pane send-text w9N:p2 -- run the tests") || !strings.Contains(joined, "herdr pane send-keys w9N:p2 enter") {
		t.Fatalf("commands = %s", joined)
	}

	if _, _, err := executeRoot(t, "send", "pi-nope", "hi"); err == nil {
		t.Fatal("unknown target accepted")
	}
}

func TestSetupPiInstallsExtension(t *testing.T) {
	fakePiHost(t)
	env, _ := newEnv()
	stdout, _, err := executeRoot(t, "setup", "pi")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(env.Home, ".pi", "agent", "extensions", "context-drop.ts")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "sendUserMessage") || !strings.Contains(stdout, path) {
		t.Fatalf("installed %q: %v; stdout %q", path, err, stdout)
	}
}
