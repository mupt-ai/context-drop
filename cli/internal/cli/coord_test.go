package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
)

// coordHost stages two Pi sessions without the extension, both in one git
// worktree and each in a tmux pane: a (pid 42, pane %1) and b (pid 43, pane
// %2). This command runs as pid 500 in a shell under a, so a is "self".
type coordHost struct {
	repo     string
	a, b     string // session IDs
	mu       sync.Mutex
	procs    map[int]agents.Process
	polls    int
	onPoll   func(n int, procs map[int]agents.Process) error
	commands []string
	signals  []string
}

func newCoordHost(t *testing.T) *coordHost {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", filepath.Join(home, ".context-drop"))
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	h := &coordHost{
		repo: repo,
		a:    agents.MakeID(agents.Pi, fmt.Sprintf("pid-42-%d", start.Unix())),
		b:    agents.MakeID(agents.Pi, fmt.Sprintf("pid-43-%d", start.Unix())),
		procs: map[int]agents.Process{
			42:  {PID: 42, PPID: 1, TTY: "/dev/ttys001", Command: "pi", Start: start},
			43:  {PID: 43, PPID: 1, TTY: "/dev/ttys002", Command: "pi", Start: start},
			41:  {PID: 41, PPID: 42, Command: "zsh"},
			500: {PID: 500, PPID: 41, Command: "context-drop"},
		},
	}
	oldEnv, oldProviders, oldSelf := newEnv, allProviders, selfPID
	t.Cleanup(func() { newEnv, allProviders, selfPID = oldEnv, oldProviders, oldSelf })
	allProviders = func() []agents.Provider { return agents.Providers() }
	selfPID = func() int { return 500 }
	newEnv = func() (*agents.Env, error) {
		return &agents.Env{
			Home: home,
			Run: func(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
				cmd := strings.Join(append([]string{name}, args...), " ")
				h.mu.Lock()
				h.commands = append(h.commands, cmd)
				h.mu.Unlock()
				switch {
				case strings.HasPrefix(cmd, "tmux list-panes"):
					return []byte("%1\t/dev/ttys001\twork\t1\t@1\ta\t0\n%2\t/dev/ttys002\twork\t2\t@2\tb\t0\n"), nil
				case strings.HasPrefix(cmd, "tmux send-keys"):
					return nil, nil
				}
				return nil, os.ErrNotExist
			},
			Processes: func(context.Context) (map[int]agents.Process, error) {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.polls++
				if h.onPoll != nil {
					if err := h.onPoll(h.polls, h.procs); err != nil {
						return nil, err
					}
				}
				out := map[int]agents.Process{}
				for pid, p := range h.procs {
					out[pid] = p
				}
				return out, nil
			},
			OpenFiles: func(context.Context, []string) (map[string][]int, error) { return nil, nil },
			Cwds: func(context.Context, []int) (map[int]string, error) {
				return map[int]string{42: repo, 43: filepath.Join(repo, "src")}, nil
			},
			Getenv: func(k string) string {
				if k == "CONTEXT_DROP_HOME" {
					return filepath.Join(home, ".context-drop")
				}
				return ""
			},
			Signal: func(pid int, sig syscall.Signal) error {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.signals = append(h.signals, fmt.Sprint(sig, " ", pid))
				delete(h.procs, pid)
				return nil
			},
		}, nil
	}
	return h
}

func (h *coordHost) ran(prefix string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestSendAllSkipsTheSender(t *testing.T) {
	h := newCoordHost(t)
	_, stderr, err := executeRoot(t, "send", "--all", "--in", "tmux", "rebase", "on", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !h.ran("tmux send-keys -t %2 -l -- rebase on main") || h.ran("tmux send-keys -t %1") {
		t.Fatalf("commands = %q", h.commands)
	}
	if !strings.Contains(stderr, "sent to "+h.b) || strings.Contains(stderr, h.a) {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, _, err := executeRoot(t, "send", "--in", "tmux", "hi"); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("a filter without --all: %v", err)
	}
	if _, _, err := executeRoot(t, "send", "--all", "--session", "nowhere", "hi"); err == nil {
		t.Fatal("an empty group was accepted")
	}
}

func TestStopRefusesSelfAndStopsOthers(t *testing.T) {
	h := newCoordHost(t)
	if _, _, err := executeRoot(t, "stop", h.a); err == nil {
		t.Fatal("stopped its own session without --force")
	}
	stdout, _, err := executeRoot(t, "stop", h.b, "--timeout", "2s")
	if err != nil || !strings.Contains(stdout, "stopped "+h.b+" (sent SIGTERM to pi (pid 43))") {
		t.Fatalf("stop = %q, %v", stdout, err)
	}
	if strings.Join(h.signals, ",") != "terminated 43" {
		t.Fatalf("signals = %q", h.signals)
	}
	if _, _, err := executeRoot(t, "stop", "--all"); err == nil {
		t.Fatal("--all stopped the caller's own session, or found someone else to stop")
	}
	if len(h.signals) != 1 {
		t.Fatalf("signals = %q", h.signals)
	}
}

func TestClaimListAndRelease(t *testing.T) {
	h := newCoordHost(t)
	src := filepath.Join(h.repo, "src")
	stdout, _, err := executeRoot(t, "claim", src, "--note", "splitting the parser")
	if err != nil || !strings.Contains(stdout, h.a+" claimed") {
		t.Fatalf("claim = %q, %v", stdout, err)
	}
	_, _, err = executeRoot(t, "claim", filepath.Join(src, "parse.go"), "--as", h.b)
	if err == nil || !strings.Contains(err.Error(), "claimed by "+h.a) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overlapping claim: %v", err)
	}

	stdout, _, err = executeRoot(t, "claims", "--json")
	var claims []agents.Claim
	if err != nil || json.Unmarshal([]byte(stdout), &claims) != nil || len(claims) != 1 || claims[0].Note != "splitting the parser" {
		t.Fatalf("claims = %s, %v", stdout, err)
	}
	stdout, stderr, err := executeRoot(t, "list")
	if err != nil || !strings.Contains(stdout, "CLAIMS") || !strings.Contains(stdout, src) {
		t.Fatalf("list = %q, %v", stdout, err)
	}
	if !strings.Contains(stderr, "warning: "+h.a+", "+h.b+" share") && !strings.Contains(stderr, "warning: "+h.b+", "+h.a+" share") {
		t.Fatalf("list stderr = %q", stderr)
	}

	stdout, _, err = executeRoot(t, "overlaps")
	if err != nil || !strings.Contains(stdout, "worktree") {
		t.Fatalf("overlaps = %q, %v", stdout, err)
	}

	stdout, _, err = executeRoot(t, "release")
	if err != nil || !strings.Contains(stdout, h.a+" released") {
		t.Fatalf("release = %q, %v", stdout, err)
	}
	if stdout, _, _ = executeRoot(t, "claims"); !strings.Contains(stdout, "No claims") {
		t.Fatalf("after release = %q", stdout)
	}
}

func TestClaimOutsideAnAgentNeedsAs(t *testing.T) {
	newCoordHost(t)
	selfPID = func() int { return 999 }
	if _, _, err := executeRoot(t, "claim", "docs"); err == nil || !strings.Contains(err.Error(), "--as") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchReportsChanges(t *testing.T) {
	h := newCoordHost(t)
	h.onPoll = func(n int, procs map[int]agents.Process) error {
		switch {
		case n == 2: // ps fails once; that pass must not read as everyone ending
			return fmt.Errorf("ps: interrupted")
		case n == 3: // then b really exits
			delete(procs, 43)
		}
		return nil
	}
	stdout, _, err := executeRoot(t, "watch", "--for", "150ms", "--interval", "10ms", "--json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var c agents.Change
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &c) != nil || c.Event != agents.ChangeEnded || c.ID != h.b {
		t.Fatalf("watch = %q", stdout)
	}
}

func TestGroupsSkipEverySessionTheCallerMayBe(t *testing.T) {
	old := selfPID
	t.Cleanup(func() { selfPID = old })
	selfPID = func() int { return 500 }
	// The caller runs under a Codex app-server (100) that serves two threads.
	env := &agents.Env{Processes: func(context.Context) (map[int]agents.Process, error) {
		return map[int]agents.Process{500: {PID: 500, PPID: 100}, 100: {PID: 100, PPID: 1}}, nil
	}}
	idx := agents.Index{Sessions: []agents.Session{{ID: "cx-1", PID: 100}, {ID: "cx-2", PID: 100}, {ID: "cc-3", PID: 300}}}
	got := withoutSelf(context.Background(), env, idx, idx.Sessions)
	if len(got) != 1 || got[0].ID != "cc-3" {
		t.Fatalf("group = %+v", got)
	}
}

func TestWatchWithAPlaceKeepsSessionsWhosePaneWasMissed(t *testing.T) {
	prev := []agents.Session{{ID: "a", Location: &agents.Location{Mux: agents.MuxTmux, Pane: "%1"}}}
	idx := agents.Index{Sessions: []agents.Session{{ID: "a"}, {ID: "b"}}}
	got := keepUnlocated(nil, prev, idx, true)
	if len(got) != 1 || got[0].ID != "a" || got[0].Location == nil {
		t.Fatalf("kept = %+v", got)
	}
	if got := keepUnlocated(nil, prev, agents.Index{}, true); len(got) != 0 {
		t.Fatalf("an ended session was kept: %+v", got)
	}
}
