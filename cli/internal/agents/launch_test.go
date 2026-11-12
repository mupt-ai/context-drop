package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// launchHost fakes a multiplexer whose launch command starts a Claude Code
// process (pid 900) that registers in ~/.claude/sessions.
func launchHost(t *testing.T, mux string, create func(args []string) []byte) (*fakeHost, string) {
	t.Helper()
	h := newFakeHost(t)
	h.vars = map[string]string{"CONTEXT_DROP_REX_BIN": "rex"}
	dir := t.TempDir()
	h.respond = func(args []string) ([]byte, error) {
		if out := create(args); out != nil {
			h.procs[900] = Process{PID: 900, TTY: "/dev/ttys050", Command: "claude", Start: time.Now()}
			h.write(".claude/sessions/900.json", fmt.Sprintf(`{"pid":900,"sessionId":"launched","cwd":%q,"status":"idle"}`, dir))
			return out, nil
		}
		if args[0] == "tmux" && len(args) > 1 && args[1] == "new-window" {
			return nil, fmt.Errorf("can't find session: context-drop")
		}
		return nil, nil
	}
	return h, dir
}

func newEnvFor(h *fakeHost) func() (*Env, error) { return func() (*Env, error) { return h.env(), nil } }

func TestLaunchInEachMultiplexer(t *testing.T) {
	cases := []struct {
		mux       string
		create    func(args []string) []byte
		wantPane  string
		wantCmd   string // the command that carries the agent
		wantShell bool   // herdr runs the agent through the pane's shell
	}{
		{
			mux:      MuxTmux,
			create:   func(a []string) []byte { return when(a[0] == "tmux" && a[1] == "new-session", "%7\n") },
			wantPane: "%7",
			wantCmd:  "tmux new-session -d -s context-drop -n claude-x -c DIR -P -F #{pane_id}\t#{session_name}\t#{window_index} -- dari claude --model opus",
		},
		{
			mux: MuxHerdr,
			create: func(a []string) []byte {
				return when(a[0] == "herdr" && a[1] == "tab", `{"result":{"root_pane":{"pane_id":"w1:p9"}}}`)
			},
			wantPane:  "w1:p9",
			wantCmd:   "herdr pane run w1:p9 dari claude --model opus",
			wantShell: true,
		},
		{
			mux:      MuxRex,
			create:   func(a []string) []byte { return when(a[0] == "rex" && a[1] == "run", `{"block_ids":["block:abc"]}`) },
			wantPane: "block:abc",
			wantCmd:  "rex run --cwd DIR --label claude-x --json -- dari claude --model opus",
		},
	}
	for _, c := range cases {
		t.Run(c.mux, func(t *testing.T) {
			h, dir := launchHost(t, c.mux, c.create)
			res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
				Kind: Claude, Dir: dir, Name: "claude-x", Mux: c.mux, Command: "dari {agent}", Args: []string{"--model", "opus"},
				Timeout: 5 * time.Second, Interval: time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Registered || res.Session.ID != MakeID(Claude, "launched") || res.Pane != c.wantPane || res.Mux != c.mux {
				t.Fatalf("result = %+v", res)
			}
			want := strings.ReplaceAll(c.wantCmd, "DIR", dir)
			var found bool
			for _, cmd := range h.commands {
				if strings.Join(cmd, " ") == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %q in %q", want, h.commands)
			}
		})
	}
}

func when(cond bool, out string) []byte {
	if cond {
		return []byte(out)
	}
	return nil
}

func TestLaunchReportsUnregisteredAgentAfterTimeout(t *testing.T) {
	h := newFakeHost(t)
	h.respond = func(args []string) ([]byte, error) { return []byte("%3\n"), nil }
	res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
		Kind: Claude, Dir: t.TempDir(), Name: "x", Mux: MuxTmux, Timeout: 20 * time.Millisecond, Interval: time.Millisecond,
	})
	if err != nil || res.Registered || res.Pane != "%3" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestAgentArgv(t *testing.T) {
	env := newFakeHost(t).env()
	cases := []struct {
		opts LaunchOptions
		want string
	}{
		{LaunchOptions{Kind: Codex}, "codex"},
		{LaunchOptions{Kind: Pi, Command: "dari {agent}"}, "dari|pi"},
		{LaunchOptions{Kind: Claude, Command: `env FOO='a b' claude`, Args: []string{"--x"}}, "env|FOO=a b|claude|--x"},
		{LaunchOptions{Kind: OpenCode, Prompt: "fix it"}, "opencode|--prompt|fix it"},
	}
	for _, c := range cases {
		argv, err := agentArgv(env, c.opts)
		if err != nil || strings.Join(argv, "|") != c.want {
			t.Errorf("agentArgv(%+v) = %q, %v; want %q", c.opts, argv, err, c.want)
		}
	}
	if _, err := agentArgv(env, LaunchOptions{Kind: "muse"}); err == nil {
		t.Error("unsupported agent accepted")
	}
	if _, err := agentArgv(env, LaunchOptions{Kind: Claude, Command: `claude "open`}); err == nil {
		t.Error("unterminated quote accepted")
	}
	if got := shellJoin([]string{"dari", "claude", "--name", "it's here"}); got != `dari claude --name 'it'\''s here'` {
		t.Errorf("shellJoin = %s", got)
	}
}

func TestPiInRexBlockIsTypedIntoThroughItsWrapper(t *testing.T) {
	h := newFakeHost(t)
	h.vars = map[string]string{"CONTEXT_DROP_REX_BIN": "rex"}
	// `dari pi` (pid 60) is the rex block's foreground process and pi (pid 61) is its child.
	h.procs[60] = Process{PID: 60, PPID: 50, Command: "dari pi"}
	h.procs[61] = Process{PID: 61, PPID: 60, TTY: "/dev/ttys070", Command: "pi", Start: time.Now().Add(-time.Minute)}
	h.cwds[61] = "/work/r"
	h.write(".pi/agent/sessions/--work-r--/s.jsonl", jsonl(map[string]any{"type": "session", "id": "rex-pi"}))
	h.respond = func(args []string) ([]byte, error) {
		switch cmd := strings.Join(args, " "); {
		case args[0] == "tmux" || args[0] == "herdr":
			return nil, fmt.Errorf("not running")
		case strings.HasSuffix(cmd, " ls --json"):
			return []byte(`{"sessions":[{"session_id":"session:s1","label":"calm"}]}`), nil
		case strings.Contains(cmd, " block ls -s session:s1"):
			return []byte(`{"blocks":[{"block_id":"block:one"},{"block_id":"block:two","label":"pi"}]}`), nil
		case strings.HasSuffix(cmd, "block call process -s session:s1 -b block:two"):
			return []byte(`{"child":{"pid":50},"foreground":{"pid":60}}`), nil
		case strings.Contains(cmd, "block call process"):
			return []byte(`{"child":{"pid":10},"foreground":{"pid":11}}`), nil
		}
		return nil, nil
	}
	env := h.env()
	idx := Discover(context.Background(), env, []Provider{piProvider{}})
	if len(idx.Sessions) != 1 || idx.Sessions[0].Delivery != "rex pane block:two" {
		t.Fatalf("sessions = %+v", idx.Sessions)
	}
	if _, err := Send(context.Background(), env, Providers(), idx, idx.Sessions[0], "go", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	n := len(h.commands)
	// Blocks outside the caller's session are only found with -s.
	if got := strings.Join(h.commands[n-2], " "); got != "rex send -s session:s1 -b block:two go" {
		t.Fatalf("send = %q", got)
	}
	if got := strings.Join(h.commands[n-1], " "); got != "rex send-key -s session:s1 -b block:two enter" {
		t.Fatalf("key = %q", got)
	}
}

func TestPickLaunchedNeverClaimsAnOlderSession(t *testing.T) {
	env := newFakeHost(t).env()
	launched := time.Now()
	old := Session{ID: "oc-old", NativeID: "old", StartedAt: launched.Add(-time.Hour)}
	newer := Session{ID: "oc-new", NativeID: "new", StartedAt: launched.Add(time.Second)}
	undated := Session{ID: "cx-undated", NativeID: "u"}
	undated2 := Session{ID: "cx-undated2", NativeID: "u2"}
	cases := []struct {
		name  string
		fresh []Session
		want  string // "" means nothing is claimed yet
	}{
		{"an older session that became visible is not the launch", []Session{old}, ""},
		{"a session started after the launch is", []Session{old, newer}, "oc-new"},
		{"one session without a start time is accepted", []Session{undated}, "cx-undated"},
		{"several without start times wait for more evidence", []Session{undated, undated2}, ""},
	}
	for _, c := range cases {
		got, ok := pickLaunched(context.Background(), env, tmuxMux{}, c.fresh, Location{Mux: MuxTmux, Pane: "%1"}, launched)
		if (c.want == "") == ok || (ok && got.ID != c.want) {
			t.Errorf("%s: got %q ok=%v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}

func TestSettleWaitsForPiSessionFileID(t *testing.T) {
	polls := 0
	start := Session{ID: MakeID(Pi, "pid-77-100"), Agent: Pi, NativeID: "pid-77-100", PID: 77, StartedAt: time.Unix(100, 0)}
	got := Settle(context.Background(), func() (*Env, error) { return &Env{}, nil }, []Provider{renamingProvider{&polls}}, start, 5*time.Second, time.Millisecond)
	if got.NativeID != "file-session" {
		t.Fatalf("settled = %+v", got)
	}
	// Sessions whose ID never changes are returned as is, without polling.
	claude := Session{ID: "cc-1", Agent: Claude, NativeID: "abc"}
	if got := Settle(context.Background(), func() (*Env, error) { t.Fatal("polled"); return nil, nil }, nil, claude, time.Second, 0); got.ID != "cc-1" {
		t.Fatalf("claude = %+v", got)
	}
}
