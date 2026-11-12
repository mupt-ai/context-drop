package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLaunchAtPlacement(t *testing.T) {
	cases := []struct {
		name string
		mux  string
		at   Placement
		// reply answers the multiplexer's commands; the command that opens the
		// pane must return a pane.
		reply func(cmd string) string
		want  []string // commands that must have run, with DIR for the directory
	}{
		{
			name:  "rex session opens a new window there",
			mux:   MuxRex,
			at:    Placement{Session: "calm-prairie"},
			reply: func(string) string { return `{"window_id":"window:w","block_ids":["block:new"]}` },
			want:  []string{"rex run -s calm-prairie --cwd DIR --label claude-x --json -- claude"},
		},
		{
			name:  "rex block opens a split beside it",
			mux:   MuxRex,
			at:    Placement{Session: "calm-prairie", Pane: "reviewer"},
			reply: func(string) string { return `{"block_ids":["block:new"]}` },
			want:  []string{"rex run -s calm-prairie -b reviewer --split right --cwd DIR --label claude-x --json -- claude"},
		},
		{
			name:  "rex window needs a split",
			mux:   MuxRex,
			at:    Placement{Window: "window:w"},
			reply: func(string) string { return `{"block_ids":["block:new"]}` },
			want:  []string{"rex run -w window:w --split right --cwd DIR --label claude-x --json -- claude"},
		},
		{
			name:  "tmux session opens a window there",
			mux:   MuxTmux,
			at:    Placement{Session: "work"},
			reply: func(string) string { return "%9\twork\t3\n" },
			want:  []string{"tmux new-window -t work: -n claude-x -c DIR -P -F #{pane_id}\t#{session_name}\t#{window_index} -- claude"},
		},
		{
			name:  "tmux window opens a split",
			mux:   MuxTmux,
			at:    Placement{Session: "work", Window: "2"},
			reply: func(string) string { return "%9\twork\t2\n" },
			want:  []string{"tmux split-window -h -t work:2 -c DIR -P -F #{pane_id}\t#{session_name}\t#{window_index} -- claude"},
		},
		{
			name:  "tmux pane ID is used as the target",
			mux:   MuxTmux,
			at:    Placement{Session: "work", Pane: "%5"},
			reply: func(string) string { return "%9\twork\t2\n" },
			want:  []string{"tmux split-window -h -t %5 -c DIR -P -F #{pane_id}\t#{session_name}\t#{window_index} -- claude"},
		},
		{
			name: "herdr workspace label resolves to its ID",
			mux:  MuxHerdr,
			at:   Placement{Session: "ctxws"},
			reply: func(cmd string) string {
				if strings.HasPrefix(cmd, "herdr workspace list") {
					return `{"result":{"workspaces":[{"workspace_id":"w1","label":"ctxws"}]}}`
				}
				return `{"result":{"root_pane":{"pane_id":"w1:p3","tab_id":"w1:t3","workspace_id":"w1"}}}`
			},
			want: []string{"herdr tab create --cwd DIR --label claude-x --workspace w1", "herdr pane run w1:p3 claude"},
		},
		{
			name: "herdr tab splits its focused pane",
			mux:  MuxHerdr,
			at:   Placement{Window: "notes"},
			reply: func(cmd string) string {
				switch {
				case strings.HasPrefix(cmd, "herdr tab list"):
					return `{"result":{"tabs":[{"tab_id":"w1:t2","label":"notes"}]}}`
				case strings.HasPrefix(cmd, "herdr pane list"):
					return `{"result":{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","focused":true},{"pane_id":"w1:p2","tab_id":"w1:t2"},{"pane_id":"w1:p5","tab_id":"w1:t2","focused":true}]}}`
				}
				return `{"result":{"pane":{"pane_id":"w1:p6","tab_id":"w1:t2","workspace_id":"w1"}}}`
			},
			want: []string{"herdr pane split w1:p5 --direction right --cwd DIR", "herdr pane rename w1:p6 claude-x", "herdr pane run w1:p6 claude"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.vars = map[string]string{"CONTEXT_DROP_REX_BIN": "rex"}
			h.respond = func(args []string) ([]byte, error) { return []byte(c.reply(strings.Join(args, " "))), nil }
			dir := t.TempDir()
			res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
				Kind: Claude, Dir: dir, Name: "claude-x", Mux: c.mux, At: c.at,
			})
			if err != nil || res.Pane == "" || res.Status != LaunchStarted {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			ran := map[string]bool{}
			for _, cmd := range h.commands {
				ran[strings.Join(cmd, " ")] = true
			}
			for _, w := range c.want {
				if w = strings.ReplaceAll(w, "DIR", dir); !ran[w] {
					t.Errorf("missing %q in %q", w, h.commands)
				}
			}
		})
	}
}

func TestLaunchInMissingTmuxSessionFails(t *testing.T) {
	h := newFakeHost(t)
	h.respond = func(args []string) ([]byte, error) { return nil, fmt.Errorf("can't find session: work") }
	_, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
		Kind: Claude, Dir: t.TempDir(), Name: "x", Mux: MuxTmux, At: Placement{Session: "work"},
	})
	if err == nil {
		t.Fatal("a named session that does not exist was accepted")
	}
	for _, cmd := range h.commands {
		if cmd[1] == "new-session" {
			t.Fatalf("created a session for an explicit target: %q", cmd)
		}
	}
}

const (
	claudeTrustNo = `
 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team).

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel`
	claudeTrustYes = `
 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team).

   No, exit
 ❯ Yes, I trust this folder

 Enter to confirm · Esc to cancel`
	codexTrust = `
  > You are in /tmp/x

  Do you trust the contents of this directory? Working with untrusted contents
  comes with higher risk of prompt injection.

› 1. Yes, continue
  2. No, quit

  Press enter to continue`
	codexReady = `
  >_ OpenAI Codex (v0.167.1)

› Ask Codex to do anything

  ? for shortcuts`
)

func TestFirstRunScreens(t *testing.T) {
	cases := []struct {
		screen, kind, key string
	}{
		{claudeTrustNo, ScreenTrust, "down"},
		{claudeTrustYes, ScreenTrust, "enter"},
		{codexTrust, ScreenTrust, "enter"},
		{"Do you trust the files in this folder?\n❯ 1. Yes, allow Codex to work in this folder without asking for approval", ScreenTrust, ""},
		{"Select login method:\n❯ 1. Claude account with subscription", ScreenSetup, ""},
		{codexReady, "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := firstRunScreen(c.screen); got != c.kind {
			t.Errorf("firstRunScreen(%q) = %q, want %q", c.screen, got, c.kind)
		}
		if got := trustKey(c.screen); got != c.key {
			t.Errorf("trustKey(%q) = %q, want %q", c.screen, got, c.key)
		}
	}
	// Only the bottom of the pane counts: a trust prompt long scrolled away is not one.
	old := claudeTrustYes + strings.Repeat("\nlater output", firstRunLines)
	if got := firstRunScreen(old); got != "" {
		t.Errorf("scrolled-away prompt = %q", got)
	}
}

// trustHost fakes a tmux pane whose agent shows screens in turn; the agent
// registers once the screen no longer asks anything.
func trustHost(t *testing.T, screens map[string]string, first string) (*fakeHost, string, *[]string) {
	h := newFakeHost(t)
	dir := t.TempDir()
	screen, keys := first, &[]string{}
	h.respond = func(args []string) ([]byte, error) {
		switch {
		case args[1] == "new-window":
			return []byte("%4\tcontext-drop\t1\n"), nil
		case args[1] == "capture-pane":
			return []byte(screen), nil
		case args[1] == "send-keys":
			key := args[len(args)-1]
			*keys = append(*keys, key)
			screen = screens[screen+"+"+key]
			if firstRunScreen(screen) == "" {
				h.procs[900] = Process{PID: 900, TTY: "/dev/ttys050", Command: "claude", Start: time.Now()}
				h.write(".claude/sessions/900.json", fmt.Sprintf(`{"pid":900,"sessionId":"trusted","cwd":%q,"status":"idle"}`, dir))
			}
		}
		return nil, nil
	}
	return h, dir, keys
}

func TestLaunchReportsTrustPrompt(t *testing.T) {
	h, dir, keys := trustHost(t, nil, claudeTrustNo)
	var reported []string
	res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
		Kind: Claude, Dir: dir, Name: "x", Mux: MuxTmux, Timeout: 30 * time.Millisecond, Interval: time.Millisecond,
		OnScreen: func(screen string, at Location) { reported = append(reported, screen+" "+at.Pane) },
	})
	if err != nil || res.Status != LaunchWaitingForTrust || res.Registered {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(reported) != 1 || reported[0] != "trust %4" {
		t.Fatalf("reported %q, want one trust report", reported)
	}
	if len(*keys) != 0 {
		t.Fatalf("pressed %q without --trust", *keys)
	}
}

func TestLaunchTrustAcceptsPrompt(t *testing.T) {
	screens := map[string]string{
		claudeTrustNo + "+Down":   claudeTrustYes,
		claudeTrustYes + "+Enter": "> ",
	}
	h, dir, keys := trustHost(t, screens, claudeTrustNo)
	res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
		Kind: Claude, Dir: dir, Name: "x", Mux: MuxTmux, Trust: true, Timeout: 5 * time.Second, Interval: time.Millisecond,
		OnScreen: func(screen string, _ Location) { t.Errorf("reported %s although --trust handled it", screen) },
	})
	if err != nil || res.Status != LaunchRegistered || res.Session.NativeID != "trusted" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Join(*keys, " ") != "Down Enter" {
		t.Fatalf("keys = %q", *keys)
	}
}

// muxHost fakes one agent process in each multiplexer: pid 10 in tmux (by
// terminal), pid 21 under herdr pane w1:p2's shell, and pid 31 under a rex block.
func muxHost(t *testing.T) *fakeHost {
	h := newFakeHost(t)
	h.vars = map[string]string{"CONTEXT_DROP_REX_BIN": "rex"}
	h.procs[10] = Process{PID: 10, TTY: "/dev/ttys010"}
	h.procs[21] = Process{PID: 21, PPID: 20}
	h.procs[31] = Process{PID: 31, PPID: 30}
	h.respond = func(args []string) ([]byte, error) {
		switch cmd := strings.Join(args, " "); {
		case strings.HasPrefix(cmd, "tmux list-panes"):
			return []byte("%3\t/dev/ttys010\twork\t2\t@7\teditor\t1\n"), nil
		case cmd == "herdr pane list":
			return []byte(`{"result":{"panes":[{"pane_id":"w1:p2","tab_id":"w1:t1","workspace_id":"w1"}]}}`), nil
		case cmd == "herdr workspace list":
			return []byte(`{"result":{"workspaces":[{"workspace_id":"w1","label":"api"}]}}`), nil
		case cmd == "herdr tab list":
			return []byte(`{"result":{"tabs":[{"tab_id":"w1:t1","label":"agents"}]}}`), nil
		case strings.HasPrefix(cmd, "herdr pane process-info"):
			return []byte(`{"result":{"process_info":{"shell_pid":20}}}`), nil
		case strings.HasSuffix(cmd, " ls --json"):
			return []byte(`{"sessions":[{"session_id":"session:01a1b8fb-603e","label":"calm-prairie"}]}`), nil
		case strings.Contains(cmd, "block ls"):
			return []byte(`{"blocks":[{"block_id":"block:01a1cbb1-08ae-7f3e-8fb3-ae32e20a00ee","label":"reviewer","window_id":"window:9"}]}`), nil
		case strings.Contains(cmd, "block call process"):
			return []byte(`{"child":{"pid":30},"foreground":{"pid":30}}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", strings.Join(args, " "))
	}
	return h
}

func TestLocateFindsAgentsInEveryMultiplexer(t *testing.T) {
	h := muxHost(t)
	sessions := []Session{{ID: "a", PID: 10, TTY: "/dev/ttys010"}, {ID: "b", PID: 21}, {ID: "c", PID: 31}, {ID: "d", PID: 99}, {ID: "e"}}
	Locate(context.Background(), h.env(), sessions)
	want := []string{"tmux work:2 %3", "herdr w1:p2", "rex calm-prairie/reviewer", "", ""}
	for i, s := range sessions {
		got := ""
		if s.Location != nil {
			got = s.Location.String()
		}
		if got != want[i] {
			t.Errorf("%s located at %q, want %q", s.ID, got, want[i])
		}
	}
	for _, cmd := range h.commands {
		if cmd[0] == "rex" && cmd[1] != "--autostart=false" {
			t.Errorf("rex discovery may start a server: %q", cmd)
		}
	}

	tmux, herdr, rex := *sessions[0].Location, *sessions[1].Location, *sessions[2].Location
	matches := []struct {
		loc  Location
		at   Placement
		want bool
	}{
		{tmux, Placement{Session: "work", Window: "2"}, true},
		{tmux, Placement{Window: "editor"}, true},
		{tmux, Placement{Window: "@7", Pane: "%3"}, true},
		{tmux, Placement{Session: "play"}, false},
		{tmux, Placement{Pane: "editor"}, false}, // a window name is not a pane name
		{herdr, Placement{Session: "api", Window: "agents"}, true},
		{herdr, Placement{Session: "w1", Pane: "w1:p2"}, true},
		{herdr, Placement{Pane: "w1:p"}, false},
		{rex, Placement{Session: "calm-prairie", Pane: "reviewer"}, true},
		{rex, Placement{Session: "session:01a1b8fb-603e"}, true},
		{rex, Placement{Pane: "e32e20a00ee"}, true},                          // the short ID rex prints
		{rex, Placement{Pane: "01a1cbb1-08ae-7f3e-8fb3-ae32e20a00ee"}, true}, // the full ID without its kind
		{rex, Placement{Pane: "01a1cbb1-08ae"}, false},                       // prefixes are shared by blocks made near each other
		{rex, Placement{Pane: "block:01a1cbb1"}, false},
		{rex, Placement{Pane: "a00ee"}, false}, // too short to be an ID
		{rex, Placement{Session: "calm"}, false},
	}
	for _, m := range matches {
		if got := m.loc.Matches(m.at); got != m.want {
			t.Errorf("%s matches %+v = %v, want %v", m.loc, m.at, got, m.want)
		}
	}
}

func TestCaptureReadsTheSessionPane(t *testing.T) {
	h := muxHost(t)
	inner := h.respond
	h.respond = func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "capture") {
			return []byte(strings.Repeat("old\n", 100) + "❯ Yes, I trust this folder\n\n\n"), nil
		}
		return inner(args)
	}
	got, err := Capture(context.Background(), h.env(), Session{ID: "c", PID: 31}, 2)
	if err != nil || got != "old\n❯ Yes, I trust this folder" {
		t.Fatalf("capture = %q, %v", got, err)
	}
	if last := strings.Join(h.commands[len(h.commands)-1], " "); last != "rex capture -s session:01a1b8fb-603e -b block:01a1cbb1-08ae-7f3e-8fb3-ae32e20a00ee --trim" {
		t.Fatalf("capture command = %q", last)
	}
	if _, err := Capture(context.Background(), h.env(), Session{ID: "d", PID: 99}, 2); err == nil {
		t.Fatal("captured a session that runs in no pane")
	}
}

func TestTmuxTarget(t *testing.T) {
	cases := map[Placement]string{
		{Session: "work", Window: "2"}:            "work:2",
		{Session: "work", Window: "2", Pane: "1"}: "work:2.1",
		{Session: "work", Pane: "%5"}:             "%5",
		{Pane: "work:2.1"}:                        "work:2.1",
		{Window: "@7"}:                            "@7",
		{Window: "3"}:                             ":3",
	}
	for at, want := range cases {
		if got := tmuxTarget(at); got != want {
			t.Errorf("tmuxTarget(%+v) = %q, want %q", at, got, want)
		}
	}
}

func TestHerdrResolvePrefersAnExactID(t *testing.T) {
	h := newFakeHost(t)
	h.respond = func(args []string) ([]byte, error) {
		// Workspace w1 is labelled "w2", and w2 exists too.
		return []byte(`{"result":{"workspaces":[{"workspace_id":"w1","label":"w2"},{"workspace_id":"w2","label":"api"}]}}`), nil
	}
	env := h.env()
	for name, want := range map[string]string{"w2": "w2", "api": "w2", "W1": "W1", "nope": "nope"} {
		if got := herdrResolve(context.Background(), env, "workspace", name, ""); got != want {
			t.Errorf("herdrResolve(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestLaunchFindsTheSessionOfARexSplit(t *testing.T) {
	h := newFakeHost(t)
	h.vars = map[string]string{"CONTEXT_DROP_REX_BIN": "rex"}
	h.respond = func(args []string) ([]byte, error) {
		switch cmd := strings.Join(args, " "); {
		case strings.HasPrefix(cmd, "rex run"):
			return []byte(`{"anchor_id":"block:a","block_ids":["block:new"]}`), nil
		case strings.HasSuffix(cmd, " ls --json"):
			return []byte(`{"sessions":[{"session_id":"session:s0","label":"other"},{"session_id":"session:s1","label":"calm"}]}`), nil
		case strings.Contains(cmd, "block ls -s session:s1"):
			return []byte(`{"blocks":[{"block_id":"block:new","window_id":"window:w"}]}`), nil
		}
		return nil, nil
	}
	res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
		Kind: Claude, Dir: t.TempDir(), Name: "x", Mux: MuxRex, At: Placement{Pane: "reviewer"},
		Timeout: 5 * time.Millisecond, Interval: time.Millisecond,
	})
	if err != nil || res.Location.Session != "calm" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, cmd := range h.commands {
		if strings.Join(cmd, " ") == "rex capture -s session:s1 -b block:new --trim" {
			return
		}
	}
	t.Fatalf("the split was read without its session: %q", h.commands)
}

func TestLaunchDoesNotTreatAnUnreadablePaneAsReady(t *testing.T) {
	h := newFakeHost(t)
	dir := t.TempDir()
	// The agent registers at once, but its pane cannot be read at first.
	h.procs[900] = Process{PID: 900, TTY: "/dev/ttys050", Command: "claude", Start: time.Now().Add(time.Second)}
	captures := 0
	h.respond = func(args []string) ([]byte, error) {
		switch args[1] {
		case "new-window":
			h.write(".claude/sessions/900.json", fmt.Sprintf(`{"pid":900,"sessionId":"s","cwd":%q,"status":"idle"}`, dir))
			return []byte("%4\tcontext-drop\t1\n"), nil
		case "capture-pane":
			if captures++; captures == 1 {
				return nil, fmt.Errorf("can't find pane")
			}
			return []byte(claudeTrustNo), nil
		}
		return nil, nil
	}
	res, err := Launch(context.Background(), newEnvFor(h), []Provider{claudeProvider{}}, LaunchOptions{
		Kind: Claude, Dir: dir, Name: "x", Mux: MuxTmux, Timeout: 30 * time.Millisecond, Interval: time.Millisecond,
	})
	if err != nil || !res.Registered || res.Status != LaunchWaitingForTrust {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestTmuxSubmitKeepsSpacingWithinLines(t *testing.T) {
	h := newFakeHost(t)
	if err := (tmuxMux{}).submit(context.Background(), h.env(), Location{Mux: MuxTmux, Pane: "%1"}, "a  b\tc\nnext"); err != nil {
		t.Fatal(err)
	}
	if got := h.commands[0][len(h.commands[0])-1]; got != "a  b\tc next" {
		t.Fatalf("typed %q", got)
	}
}
