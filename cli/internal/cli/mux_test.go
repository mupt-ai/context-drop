package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/agents"
)

const trustScreen = " Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n"

// fakeMuxHost stages two Claude Code sessions: pid 900 in rex block
// "reviewer" of session calm-prairie, and pid 901 in tmux pane %3. Rex panes
// show screen. It returns the commands the CLI runs.
func fakeMuxHost(t *testing.T, screen string) *[]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", filepath.Join(home, ".context-drop"))
	for pid, id := range map[int]string{900: "in-rex", 901: "in-tmux"} {
		path := filepath.Join(home, ".claude", "sessions", fmt.Sprintf("%d.json", pid))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		reg := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/work/%s","status":"idle"}`, pid, id, id)
		if err := os.WriteFile(path, []byte(reg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var commands []string
	oldEnv, oldProviders := newEnv, allProviders
	t.Cleanup(func() { newEnv, allProviders = oldEnv, oldProviders })
	allProviders = func() []agents.Provider { return agents.Providers() }
	newEnv = func() (*agents.Env, error) {
		return &agents.Env{
			Home: home,
			Run: func(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
				cmd := strings.Join(append([]string{name}, args...), " ")
				commands = append(commands, cmd)
				switch {
				case strings.HasPrefix(cmd, "tmux list-panes"):
					return []byte("%3\t/dev/ttys010\twork\t2\t@7\teditor\t1\n"), nil
				case strings.HasSuffix(cmd, " ls --json"):
					return []byte(`{"sessions":[{"session_id":"session:s1","label":"calm-prairie"}]}`), nil
				case strings.Contains(cmd, "block ls"):
					return []byte(`{"blocks":[{"block_id":"block:b1","label":"reviewer","window_id":"window:w1"}]}`), nil
				case strings.Contains(cmd, "block call process"):
					return []byte(`{"child":{"pid":30},"foreground":{"pid":30}}`), nil
				case strings.HasPrefix(cmd, "rex run"):
					return []byte(`{"window_id":"window:w2","block_ids":["block:new"]}`), nil
				case strings.HasPrefix(cmd, "rex capture"):
					return []byte(screen), nil
				}
				return nil, os.ErrNotExist
			},
			Processes: func(context.Context) (map[int]agents.Process, error) {
				return map[int]agents.Process{
					900: {PID: 900, PPID: 30, TTY: "/dev/ttys009", Command: "claude"},
					901: {PID: 901, PPID: 1, TTY: "/dev/ttys010", Command: "claude"},
				}, nil
			},
			OpenFiles: func(context.Context, []string) (map[string][]int, error) { return nil, nil },
			Cwds:      func(context.Context, []int) (map[int]string, error) { return nil, nil },
			Getenv: func(k string) string {
				switch k {
				case "CONTEXT_DROP_HOME":
					return filepath.Join(home, ".context-drop")
				case "CONTEXT_DROP_REX_BIN":
					return "rex"
				}
				return ""
			},
		}, nil
	}
	return &commands
}

func TestListShowsAndFiltersByPlace(t *testing.T) {
	fakeMuxHost(t, "")
	rexID, tmuxID := agents.MakeID(agents.Claude, "in-rex"), agents.MakeID(agents.Claude, "in-tmux")

	stdout, _, err := executeRoot(t, "list")
	if err != nil || !strings.Contains(stdout, "WHERE") || !strings.Contains(stdout, "rex calm-prairie/reviewer") || !strings.Contains(stdout, "tmux work:2 %3") {
		t.Fatalf("list = %q, %v", stdout, err)
	}

	stdout, _, err = executeRoot(t, "list", "--json", "--in", "rex", "--session", "calm-prairie", "--pane", "reviewer")
	var listed []agents.Session
	if err != nil || json.Unmarshal([]byte(stdout), &listed) != nil || len(listed) != 1 || listed[0].ID != rexID ||
		listed[0].Location == nil || listed[0].Location.Pane != "block:b1" || listed[0].Location.Session != "calm-prairie" {
		t.Fatalf("list --in rex = %s (%v)", stdout, err)
	}

	stdout, _, err = executeRoot(t, "list", "--window", "editor")
	if err != nil || !strings.Contains(stdout, tmuxID) || strings.Contains(stdout, rexID) {
		t.Fatalf("list --window = %q, %v", stdout, err)
	}

	stdout, _, err = executeRoot(t, "list", "--in", "herdr")
	if err != nil || !strings.Contains(stdout, "No live agent sessions") {
		t.Fatalf("list --in herdr = %q, %v", stdout, err)
	}
}

func TestReadScreenPrintsThePane(t *testing.T) {
	commands := fakeMuxHost(t, "booting\n"+trustScreen)
	stdout, _, err := executeRoot(t, "read", agents.MakeID(agents.Claude, "in-rex"), "--screen", "-n", "2")
	if err != nil || stdout != " ❯ No, exit\n   Yes, I trust this folder\n" {
		t.Fatalf("read --screen = %q, %v", stdout, err)
	}
	if last := (*commands)[len(*commands)-1]; last != "rex capture -s session:s1 -b block:b1 --trim" {
		t.Fatalf("capture = %q", last)
	}
}

func TestStartReportsTrustPromptInsteadOfSendingPrompt(t *testing.T) {
	commands := fakeMuxHost(t, trustScreen)
	_, stderr, err := executeRoot(t, "start", "claude", "--cwd", t.TempDir(), "--in", "rex", "--session", "calm-prairie",
		"--prompt", "hi", "--timeout", "50ms")
	if err == nil || !strings.Contains(err.Error(), "waiting at a folder-trust prompt") || !strings.Contains(err.Error(), "the prompt was not sent") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stderr, "rex pane block:new is waiting at a folder-trust prompt") {
		t.Fatalf("stderr = %q", stderr)
	}
	var ran bool
	for _, cmd := range *commands {
		ran = ran || strings.HasPrefix(cmd, "rex run -s calm-prairie --cwd ")
		if strings.HasPrefix(cmd, "rex send") {
			t.Fatalf("typed into the agent: %q", cmd)
		}
	}
	if !ran {
		t.Fatalf("commands = %q", *commands)
	}
}
