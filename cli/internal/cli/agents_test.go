package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/herdr"
)

// fakeHerdr answers herdr CLI calls from a script keyed by the first two args.
type fakeHerdr struct {
	calls   [][]string
	answers map[string][]string // consumed in order; the last answer repeats
}

func (f *fakeHerdr) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args[:2], " ")
	queue := f.answers[key]
	if len(queue) == 0 {
		return []byte(`{"result":{}}`), nil
	}
	answer := queue[0]
	if len(queue) > 1 {
		f.answers[key] = queue[1:]
	}
	if strings.HasPrefix(answer, "ERR:") {
		return nil, fmt.Errorf("%s", strings.TrimPrefix(answer, "ERR:"))
	}
	return []byte(answer), nil
}

func (f *fakeHerdr) called(prefix ...string) [][]string {
	var out [][]string
	for _, call := range f.calls {
		if len(call) >= len(prefix) && strings.Join(call[:len(prefix)], " ") == strings.Join(prefix, " ") {
			out = append(out, call)
		}
	}
	return out
}

func useFakeHerdr(t *testing.T, answers map[string][]string) *fakeHerdr {
	t.Helper()
	f := &fakeHerdr{answers: answers}
	prevHerdr, prevSleep := newHerdr, sleep
	newHerdr = func() herdr.Client { return herdr.Client{Run: f.run} }
	sleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { newHerdr, sleep = prevHerdr, prevSleep })
	return f
}

func agentJSON(kind, status string) string {
	return fmt.Sprintf(`{"result":{"agent":{"agent":%q,"agent_status":%q,"pane_id":"w1:p9"}}}`, kind, status)
}

const workspacesJSON = `{"result":{"workspaces":[{"workspace_id":"w1","label":"dari-mono"},{"workspace_id":"w2","label":"evals"}]}}`

func TestAgentsListsNamesOrPanes(t *testing.T) {
	useFakeHerdr(t, map[string][]string{"agent list": {`{"result":{"agents":[
		{"name":"router","agent":"claude","agent_status":"working","pane_id":"w1:p1","cwd":"/r","terminal_title_stripped":"Fix router"},
		{"agent":"pi","agent_status":"idle","pane_id":"w1:p2","cwd":"/s"}]}}`}})
	out, _, err := executeRoot(t, "agents")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"router", "Fix router", "w1:p2", "idle"} {
		if !strings.Contains(out, want) {
			t.Fatalf("agents output missing %q:\n%s", want, out)
		}
	}
}

func TestNewLaunchesTrustsNamesAndPrompts(t *testing.T) {
	f := useFakeHerdr(t, map[string][]string{
		"agent list":     {`{"result":{"agents":[]}}`},
		"workspace list": {workspacesJSON},
		"tab create":     {`{"result":{"root_pane":{"pane_id":"w1:p9"}}}`},
		"agent get":      {"ERR:not an agent yet", agentJSON("claude", "blocked"), agentJSON("claude", "idle")},
		"agent read":     {"Is this a project you trust?\n ❯ No, exit\n   Yes, I trust this folder\n"},
	})
	cwd := t.TempDir()
	out, _, err := executeRoot(t, "new", "fix-router", "fix the router", "--workspace", "dari-mono", "--cwd", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started fix-router") {
		t.Fatalf("output = %q", out)
	}
	tab := f.called("tab", "create")
	if len(tab) != 1 || strings.Join(tab[0], " ") != "tab create --workspace w1 --cwd "+cwd+" --label fix-router --no-focus" {
		t.Fatalf("tab create = %v", tab)
	}
	if run := f.called("pane", "run", "w1:p9", "dari --claude --dangerously-skip-permissions"); len(run) != 1 {
		t.Fatalf("launch calls = %v", f.calls)
	}
	if keys := f.called("pane", "send-keys", "w1:p9", "down", "enter"); len(keys) != 1 {
		t.Fatalf("trust dialog not accepted: %v", f.calls)
	}
	if rename := f.called("agent", "rename", "w1:p9", "fix-router"); len(rename) != 1 {
		t.Fatalf("rename calls = %v", f.calls)
	}
	prompt := f.called("agent", "prompt")
	if len(prompt) != 1 || strings.Join(prompt[0], " ") != "agent prompt fix-router fix the router" {
		t.Fatalf("prompt calls = %v", prompt)
	}
}

func TestNewRefusesAmbiguousOrMissingTargets(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name    string
		answers map[string][]string
		args    []string
		want    string
	}{
		{"unknown workspace", map[string][]string{"agent list": {`{"result":{"agents":[]}}`}, "workspace list": {workspacesJSON}},
			[]string{"new", "task", "p", "--workspace", "nope", "--cwd", cwd}, `no herdr workspace labeled "nope"; have: dari-mono, evals`},
		{"duplicate workspace", map[string][]string{"agent list": {`{"result":{"agents":[]}}`}, "workspace list": {`{"result":{"workspaces":[{"workspace_id":"w1","label":"x"},{"workspace_id":"w2","label":"x"}]}}`}},
			[]string{"new", "task", "p", "--workspace", "x", "--cwd", cwd}, "2 herdr workspaces are labeled"},
		{"name taken", map[string][]string{"agent list": {`{"result":{"agents":[{"name":"task","pane_id":"w1:p1"}]}}`}},
			[]string{"new", "task", "p", "--workspace", "x", "--cwd", cwd}, "already exists at w1:p1"},
		{"relative cwd", nil, []string{"new", "task", "p", "--workspace", "x", "--cwd", "rel"}, "absolute"},
		{"bad name", nil, []string{"new", "Bad Name", "p", "--workspace", "x", "--cwd", cwd}, "invalid name"},
		{"bad agent", nil, []string{"new", "task", "p", "--workspace", "x", "--cwd", cwd, "--agent", "gpt"}, "unknown agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := useFakeHerdr(t, tc.answers)
			_, _, err := executeRoot(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.called("tab", "create")) != 0 {
				t.Fatal("opened a tab despite the error")
			}
		})
	}
}

func TestNewFailsWhenAgentBlocksOnSomethingElse(t *testing.T) {
	useFakeHerdr(t, map[string][]string{
		"agent list":     {`{"result":{"agents":[]}}`},
		"workspace list": {workspacesJSON},
		"tab create":     {`{"result":{"root_pane":{"pane_id":"w1:p9"}}}`},
		"agent get":      {agentJSON("codex", "blocked")},
		"agent read":     {"Sign in with ChatGPT\n"},
	})
	_, _, err := executeRoot(t, "new", "task", "p", "--workspace", "dari-mono", "--cwd", t.TempDir(), "--agent", "codex")
	if err == nil || !strings.Contains(err.Error(), "Sign in with ChatGPT") || !strings.Contains(err.Error(), "tab left open at w1:p9") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendRefusesBusyAgentUnlessForced(t *testing.T) {
	f := useFakeHerdr(t, map[string][]string{"agent get": {agentJSON("claude", "working")}})
	if _, _, err := executeRoot(t, "send", "router", "also do x"); err == nil || !strings.Contains(err.Error(), "router is working") {
		t.Fatalf("err = %v", err)
	}
	if len(f.called("agent", "prompt")) != 0 {
		t.Fatal("prompted a busy agent")
	}
	if _, _, err := executeRoot(t, "send", "router", "also do x", "--force"); err != nil {
		t.Fatal(err)
	}
	if len(f.called("agent", "prompt", "router", "also do x")) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestSendWaitPrintsSettledOutput(t *testing.T) {
	f := useFakeHerdr(t, map[string][]string{
		"agent get":  {agentJSON("claude", "idle"), agentJSON("claude", "done")},
		"agent read": {"\n\nall tests pass\n\n   \n"},
	})
	out, _, err := executeRoot(t, "send", "router", "run tests", "--wait", "--timeout", "2m")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.called("agent", "prompt", "router", "run tests", "--wait", "--timeout", "120000")) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	if out != "router is done\n\nall tests pass\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestWaitAndRead(t *testing.T) {
	f := useFakeHerdr(t, map[string][]string{
		"agent get":  {agentJSON("claude", "blocked")},
		"agent read": {"Which deployment should I use?\n"},
	})
	out, _, err := executeRoot(t, "wait", "router")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "router is blocked") || !strings.Contains(out, "Which deployment") {
		t.Fatalf("output = %q", out)
	}
	if len(f.called("agent", "wait", "router", "--timeout")) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	if _, _, err := executeRoot(t, "read", "router", "--lines", "5"); err != nil {
		t.Fatal(err)
	}
	if len(f.called("agent", "read", "router", "--lines", "25")) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
}
