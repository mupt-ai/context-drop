package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/agents"
)

// fakeMuseHost stages one live Muse session running in tmux pane %7 and
// records the commands the CLI runs.
func fakeMuseHost(t *testing.T) *[]string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".local/share/muse/sessions/2026/11/02/sess-m")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".session.lock"), []byte("pid=42\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(
		`{"payload_type":"runtime.session.metadata","payload":{"record":{"workspace_root":"/work/m"}}}`+"\n"+
			`{"payload_type":"runtime.session","payload":{"kind":"run","event":{"kind":"started","prompt":"hello there"}}}`+"\n"+
			`{"payload_type":"runtime.session","payload":{"kind":"run","event":{"kind":"assistant_message_committed","text":"hi!"}}}`+"\n"+
			`{"payload_type":"runtime.session","payload":{"kind":"run","event":{"kind":"terminal"}}}`+"\n"), 0o644)
	var commands []string
	oldEnv, oldProviders := newEnv, allProviders
	t.Cleanup(func() { newEnv, allProviders = oldEnv, oldProviders })
	allProviders = func() []agents.Provider { return agents.Providers() }
	newEnv = func() (*agents.Env, error) {
		return &agents.Env{
			Home: home,
			Run: func(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
				commands = append(commands, strings.Join(append([]string{name}, args...), " "))
				if len(args) > 0 && args[0] == "list-panes" {
					return []byte("%7 /dev/ttys004\n"), nil
				}
				return nil, nil
			},
			Processes: func(context.Context) (map[int]agents.Process, error) {
				return map[int]agents.Process{42: {PID: 42, TTY: "/dev/ttys004", Command: "muse"}}, nil
			},
			OpenFiles: func(context.Context, []string) (map[string][]int, error) { return nil, nil },
			Cwds:      func(context.Context, []int) (map[int]string, error) { return nil, nil },
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
	commands := fakeMuseHost(t)
	id := agents.MakeID(agents.Muse, "sess-m")

	stdout, _, err := executeRoot(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []agents.Session
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || len(listed) != 1 || listed[0].ID != id || listed[0].Cwd != "/work/m" {
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
	if !strings.Contains(stderr, "sent to "+id+" via tmux pane %7") {
		t.Fatalf("stderr = %q", stderr)
	}
	joined := strings.Join(*commands, "\n")
	if !strings.Contains(joined, "tmux send-keys -t %7 -l -- run the tests") {
		t.Fatalf("commands = %s", joined)
	}

	if _, _, err := executeRoot(t, "send", "mu-nope", "hi"); err == nil {
		t.Fatal("unknown target accepted")
	}
}

func TestSetupPiInstallsExtension(t *testing.T) {
	fakeMuseHost(t)
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
