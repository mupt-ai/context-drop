package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHost is a home directory plus a scripted process table and command runner.
type fakeHost struct {
	t        *testing.T
	home     string
	procs    map[int]Process
	open     map[string][]int
	cwds     map[int]string
	commands [][]string
	extraEnv [][]string
	stdin    []string
	respond  func(args []string) ([]byte, error)
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	return &fakeHost{t: t, home: t.TempDir(), procs: map[int]Process{}, open: map[string][]int{}, cwds: map[int]string{}}
}

func (h *fakeHost) env() *Env {
	run := func(_ context.Context, extra []string, stdin []byte, name string, args ...string) ([]byte, error) {
		h.commands = append(h.commands, append([]string{name}, args...))
		h.extraEnv = append(h.extraEnv, extra)
		h.stdin = append(h.stdin, string(stdin))
		if h.respond != nil {
			return h.respond(append([]string{name}, args...))
		}
		return nil, nil
	}
	return &Env{
		Home:     h.home,
		Hostname: "testhost",
		Run: func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
			return run(ctx, nil, stdin, name, args...)
		},
		RunEnv:    run,
		Processes: func(context.Context) (map[int]Process, error) { return h.procs, nil },
		OpenFiles: func(_ context.Context, paths []string) (map[string][]int, error) {
			out := map[string][]int{}
			for _, p := range paths {
				if pids, ok := h.open[p]; ok {
					out[p] = pids
				}
			}
			return out, nil
		},
		Cwds: func(context.Context, []int) (map[int]string, error) { return h.cwds, nil },
		Getenv: func(key string) string {
			if key == "CONTEXT_DROP_HOME" {
				return filepath.Join(h.home, ".context-drop")
			}
			return ""
		},
	}
}

func (h *fakeHost) write(rel string, content string) string {
	h.t.Helper()
	path := filepath.Join(h.home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func jsonl(rows ...any) string {
	var b strings.Builder
	for _, r := range rows {
		data, _ := json.Marshal(r)
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

// shortSocketDir returns a directory short enough for a unix socket path.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cdt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// serveOnce accepts one connection, records every line, and optionally replies.
func serveOnce(t *testing.T, path, reply string) <-chan []string {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []string, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		var lines []string
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if strings.TrimSpace(line) != "" {
				lines = append(lines, strings.TrimSpace(line))
				if reply != "" {
					conn.Write([]byte(reply + "\n"))
				}
			}
			if err != nil || reply != "" {
				break
			}
		}
		got <- lines
	}()
	return got
}

func TestMakeIDIsStableAndAgentScoped(t *testing.T) {
	a := MakeID(Claude, "a1a82499-7882-4527-ab39-ff9522739499")
	if a != MakeID(Claude, "a1a82499-7882-4527-ab39-ff9522739499") {
		t.Fatal("same session produced different IDs")
	}
	if !strings.HasPrefix(a, "cc-") || len(a) != len("cc-")+8 {
		t.Fatalf("id = %q", a)
	}
	if a == MakeID(Codex, "a1a82499-7882-4527-ab39-ff9522739499") {
		t.Fatal("different agents must not share IDs")
	}
}

func TestResolve(t *testing.T) {
	idx := Index{Sessions: []Session{
		{ID: "cc-11111111", Agent: Claude, NativeID: "abc-1", Name: "alpha"},
		{ID: "cc-11112222", Agent: Claude, NativeID: "abd-2"},
		{ID: "cx-33333333", Agent: Codex, NativeID: "zzz/child-9", Subagent: true},
	}}
	cases := map[string]string{
		"cc-11111111": "cc-11111111", // exact ID
		"abd-2":       "cc-11112222", // exact native ID
		"cx-3":        "cx-33333333", // ID prefix
		"child-9":     "cx-33333333", // subagent's own native ID
		"ALPHA":       "cc-11111111", // name
	}
	for target, want := range cases {
		got, err := idx.Resolve(target)
		if err != nil || got.ID != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", target, got.ID, err, want)
		}
	}
	if _, err := idx.Resolve("cc-1111"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix err = %v", err)
	}
	if _, err := idx.Resolve("nope"); err == nil {
		t.Fatal("unknown target resolved")
	}
}

func TestParsePSAndLsof(t *testing.T) {
	procs := parsePS([]byte("  40825 32970 ttys012  Tue Nov  3 05:23:28 2026     claude --model x\n" +
		"55276     1 ??       Mon Nov  2 21:20:01 2026     codex app-server --managed-daemon\n"))
	p := procs[40825]
	if p.PPID != 32970 || p.TTY != "/dev/ttys012" || p.Command != "claude --model x" || p.Start.Day() != 3 {
		t.Fatalf("proc = %+v", p)
	}
	if procs[55276].TTY != "" {
		t.Fatalf("daemon tty = %q", procs[55276].TTY)
	}
	files := parseLsof([]byte("p55276\nf39\nn/x/a.lock\np91197\nf12\nn/x/b.lock\nn/x/a.lock\n"))
	if fmt.Sprint(files["/x/a.lock"]) != "[55276 91197]" || fmt.Sprint(files["/x/b.lock"]) != "[91197]" {
		t.Fatalf("lsof = %v", files)
	}
}

func TestClaudeDiscoverReadAndSend(t *testing.T) {
	h := newFakeHost(t)
	sockDir := shortSocketDir(t)
	socket := filepath.Join(sockDir, "40825.sock")
	h.procs[40825] = Process{PID: 40825, TTY: "/dev/ttys012", Command: "/opt/homebrew/bin/claude", Start: time.Now().Add(-time.Hour)}
	started := time.Now().Add(-time.Hour)
	h.write(".claude/sessions/40825.json", fmt.Sprintf(`{"pid":40825,"sessionId":"sess-1","cwd":"/work/app","startedAt":%d,"status":"busy","name":"app-1","messagingSocketPath":%q}`, started.UnixMilli(), socket))
	h.write(".claude/sessions/40825.deadbeef.key", `{"peerToken":"0123456789abcdef0123456789abcdef"}`)
	// A registry entry whose process is gone must not be listed.
	h.write(".claude/sessions/999.json", `{"pid":999,"sessionId":"dead","cwd":"/work/old"}`)
	h.write(".claude/projects/-work-app/sess-1.jsonl", jsonl(
		map[string]any{"type": "user", "timestamp": "2026-11-03T05:24:00Z", "message": map[string]any{"content": "fix the tests"}},
		map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"content": "<system-reminder>ignored</system-reminder>"}},
		map[string]any{"type": "user", "isMeta": true, "origin": map[string]any{"kind": "peer", "from": "context-drop"},
			"message": map[string]any{"content": "Another Claude session sent a message:\nship it\n\nThis came from another Claude session — not typed by your user."}},
		map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "Running them now."},
			map[string]any{"type": "tool_use", "name": "Bash"},
		}}},
	))
	h.write(".claude/projects/-work-app/sess-1/subagents/agent-a17.meta.json", `{"agentType":"Explore","description":"scan repo","finished":true}`)
	h.write(".claude/projects/-work-app/sess-1/subagents/agent-a17.jsonl", jsonl(map[string]any{"type": "user", "message": map[string]any{"content": "scan"}}))

	env := h.env()
	idx := Discover(context.Background(), env, []Provider{claudeProvider{}})
	if len(idx.Sessions) != 2 {
		t.Fatalf("sessions = %+v", idx.Sessions)
	}
	main, sub := idx.Sessions[0], idx.Sessions[1]
	if main.ID != MakeID(Claude, "sess-1") || main.Status != StatusWorking || main.Cwd != "/work/app" || main.Subagent {
		t.Fatalf("main = %+v", main)
	}
	if !sub.Subagent || sub.ParentID != main.ID || sub.NativeID != "sess-1/a17" || sub.Name != "scan repo" || sub.Status != StatusIdle {
		t.Fatalf("sub = %+v", sub)
	}

	msgs, err := claudeProvider{}.Transcript(context.Background(), env, main)
	if err != nil {
		t.Fatal(err)
	}
	var rendered []string
	for _, m := range msgs {
		rendered = append(rendered, m.Role+":"+m.Text)
	}
	want := []string{"user:fix the tests", "user:[from context-drop] ship it", "assistant:Running them now.\n[tool: Bash]"}
	if strings.Join(rendered, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %q", rendered)
	}

	// A subagent message goes to the parent's socket with forwarding instructions.
	got := serveOnce(t, socket, "")
	recipient, err := Send(context.Background(), env, Providers(), idx, sub, "look at auth too", SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if recipient.ID != main.ID {
		t.Fatalf("recipient = %s", recipient.ID)
	}
	lines := <-got
	if len(lines) != 2 {
		t.Fatalf("frames = %q", lines)
	}
	var auth, user map[string]any
	json.Unmarshal([]byte(lines[0]), &auth)
	json.Unmarshal([]byte(lines[1]), &user)
	if auth["type"] != "auth" || auth["token"] != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("auth frame = %v", auth)
	}
	content := str(user, "message", "content")
	if user["type"] != "user" || user["session_id"] != "sess-1" || user["from"] != "context-drop" ||
		!strings.Contains(content, `SendMessage tool (to: "a17")`) || !strings.HasSuffix(content, "look at auth too") {
		t.Fatalf("user frame = %v", user)
	}
}

func TestCodexDiscoverLivenessAndSend(t *testing.T) {
	h := newFakeHost(t)
	now := time.Now()
	locks := filepath.Join(h.home, ".codex", "thread-writer-locks")
	rollout := func(id string, meta map[string]any, extra ...any) {
		meta["id"] = id
		rows := append([]any{map[string]any{"type": "session_meta", "payload": meta}}, extra...)
		h.write(fmt.Sprintf(".codex/sessions/2026/11/02/rollout-2026-11-02T21-00-00-%s.jsonl", id), jsonl(rows...))
		h.write(".codex/thread-writer-locks/"+id+".lock", "")
	}
	// A thread in a private app-server (dari codex) with a websocket token.
	tokenFile := h.write("token", "s3cret\n")
	h.procs[200] = Process{PID: 200, Command: "codex app-server --listen ws://127.0.0.1:59770 --ws-auth capability-token --ws-token-file " + tokenFile}
	rollout("private", map[string]any{"cwd": "/work/a"},
		map[string]any{"type": "response_item", "timestamp": "2026-11-03T05:00:00Z", "payload": map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "# AGENTS.md instructions\n..."}}}},
		map[string]any{"type": "response_item", "timestamp": "2026-11-03T05:00:01Z", "payload": map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "add a flag"}}}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_started"}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": "Done."}}}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete"}},
	)
	h.open[filepath.Join(locks, "private.lock")] = []int{200}
	// Threads loaded in the shared daemon: one with an open client in its directory, one without.
	h.procs[300] = Process{PID: 300, Command: "codex app-server --listen unix:// --managed-daemon"}
	h.procs[301] = Process{PID: 301, TTY: "/dev/ttys009", Command: "codex", Start: now.Add(-time.Minute)}
	h.cwds[301] = "/work/b"
	rollout("open", map[string]any{"cwd": "/work/b"})
	rollout("closed", map[string]any{"cwd": "/work/gone"})
	rollout("child", map[string]any{"cwd": "/work/b", "source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "open"}}}, "agent_nickname": "Zeno"})
	for _, id := range []string{"open", "closed", "child"} {
		h.open[filepath.Join(locks, id+".lock")] = []int{300}
	}
	// An unlocked thread is not running anywhere.
	rollout("idle", map[string]any{"cwd": "/work/b"})

	env := h.env()
	idx := Discover(context.Background(), env, []Provider{codexProvider{}})
	var ids []string
	for _, s := range idx.Sessions {
		ids = append(ids, s.NativeID)
	}
	if strings.Join(ids, ",") != "open,child,private" {
		t.Fatalf("live threads = %v", ids)
	}
	child, _ := idx.Resolve("child")
	if !child.Subagent || child.ParentID != MakeID(Codex, "open") || child.Name != "Zeno" {
		t.Fatalf("child = %+v", child)
	}
	private, _ := idx.Resolve("private")
	if private.Status != StatusIdle || private.Cwd != "/work/a" {
		t.Fatalf("private = %+v", private)
	}
	msgs, _ := codexProvider{}.Transcript(context.Background(), env, private)
	if len(msgs) != 2 || msgs[0].Text != "add a flag" || msgs[1].Text != "Done." {
		t.Fatalf("transcript = %+v", msgs)
	}

	if _, err := Send(context.Background(), env, Providers(), idx, private, "and docs", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(h.commands[len(h.commands)-1], " ")
	if cmd != "codex queue --thread private --message and docs --remote ws://127.0.0.1:59770 --remote-auth-token-env "+codexTokenEnv {
		t.Fatalf("command = %q", cmd)
	}
	if fmt.Sprint(h.extraEnv[len(h.extraEnv)-1]) != "["+codexTokenEnv+"=s3cret]" {
		t.Fatalf("env = %v", h.extraEnv[len(h.extraEnv)-1])
	}
	if _, err := Send(context.Background(), env, Providers(), idx, child, "hi", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if cmd := strings.Join(h.commands[len(h.commands)-1], " "); cmd != "codex queue --thread child --message hi" {
		t.Fatalf("daemon command = %q", cmd)
	}
}

func TestMuseDiscoverAndSendThroughTmux(t *testing.T) {
	h := newFakeHost(t)
	h.procs[32971] = Process{PID: 32971, TTY: "/dev/ttys003", Command: "muse-bin-1.4.14 --provider echo"}
	dir := ".local/share/muse/sessions/2026/11/02/01a19fda"
	h.write(dir+"/.session.lock", "pid=32971\nhost=testhost\n")
	run := func(kind string, event map[string]any) map[string]any {
		event["kind"] = kind
		return map[string]any{"payload_type": "runtime.session", "recorded_at": 1793652623045475, "payload": map[string]any{"kind": "run", "event": event}}
	}
	h.write(dir+"/session.jsonl", jsonl(
		map[string]any{"payload_type": "runtime.session.metadata", "payload": map[string]any{"record": map[string]any{"workspace_root": "/work/m"}}},
		map[string]any{"payload_type": "session.name.changed", "payload": map[string]any{"new_name": "basalt-corvus"}},
		run("started", map[string]any{"prompt": "what's up"}),
		run("assistant_message_committed", map[string]any{"text": "Looking now."}),
		run("terminal", map[string]any{"terminal": "completed"}),
	))
	// Reminder observers are internal and skipped; a closed session's lock is empty.
	h.write(dir+"/subagent/01a19fdb/.session.lock", "pid=32971\nhost=testhost\n")
	h.write(dir+"/subagent/01a19fdb/session.jsonl", jsonl(run("started", map[string]any{"prompt": museReminderPrefix + " for the main agent."})))
	h.write(".local/share/muse/sessions/2026/11/01/old/.session.lock", "")
	h.respond = func(args []string) ([]byte, error) {
		if args[0] == "tmux" && args[1] == "list-panes" {
			return []byte("%0 /dev/ttys003\n%1 /dev/ttys009\n"), nil
		}
		return nil, nil
	}

	env := h.env()
	idx := Discover(context.Background(), env, []Provider{museProvider{}})
	if len(idx.Sessions) != 1 {
		t.Fatalf("sessions = %+v", idx.Sessions)
	}
	s := idx.Sessions[0]
	if s.NativeID != "01a19fda" || s.Name != "basalt-corvus" || s.Cwd != "/work/m" || s.Status != StatusIdle || s.Delivery != "tmux pane %0" {
		t.Fatalf("session = %+v", s)
	}
	msgs, _ := museProvider{}.Transcript(context.Background(), env, s)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Text != "Looking now." {
		t.Fatalf("transcript = %+v", msgs)
	}
	if _, err := Send(context.Background(), env, Providers(), idx, s, "line one\nline two", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	n := len(h.commands)
	if got := strings.Join(h.commands[n-2], " "); got != "tmux send-keys -t %0 -l -- line one line two" {
		t.Fatalf("paste = %q", got)
	}
	if got := strings.Join(h.commands[n-1], " "); got != "tmux send-keys -t %0 Enter" {
		t.Fatalf("submit = %q", got)
	}
}

func TestPiExtensionRegistryAndSend(t *testing.T) {
	h := newFakeHost(t)
	socket := filepath.Join(shortSocketDir(t), "pi.sock")
	h.procs[98150] = Process{PID: 98150, Command: "node /opt/homebrew/bin/pi"}
	file := h.write(".pi/agent/sessions/--work-p--/s.jsonl", jsonl(
		map[string]any{"type": "session", "id": "pi-sess", "cwd": "/work/p"},
		map[string]any{"type": "message", "timestamp": "2026-11-03T05:43:00Z", "message": map[string]any{"role": "user", "content": "rename it"}},
		map[string]any{"type": "message", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Renamed."}}}},
	))
	reg, _ := json.Marshal(piRegistry{PID: 98150, SessionID: "pi-sess", SessionFile: file, Cwd: "/work/p", Socket: socket, Status: "working", UpdatedAt: time.Now().UnixMilli()})
	h.write(".context-drop/pi/98150.json", string(reg))

	env := h.env()
	idx := Discover(context.Background(), env, []Provider{piProvider{}})
	if len(idx.Sessions) != 1 {
		t.Fatalf("sessions = %+v", idx.Sessions)
	}
	s := idx.Sessions[0]
	if s.ID != MakeID(Pi, "pi-sess") || s.Status != StatusWorking || s.Title != "rename it" {
		t.Fatalf("session = %+v", s)
	}
	msgs, _ := piProvider{}.Transcript(context.Background(), env, s)
	if len(msgs) != 2 || msgs[1].Text != "Renamed." {
		t.Fatalf("transcript = %+v", msgs)
	}

	got := serveOnce(t, socket, `{"ok":true}`)
	if _, err := Send(context.Background(), env, Providers(), idx, s, "also the tests", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if lines := <-got; len(lines) != 1 || lines[0] != `{"text":"also the tests","type":"user"}` {
		t.Fatalf("frames = %q", lines)
	}

	got = serveOnce(t, socket, `{"ok":false,"error":"busy"}`)
	if _, err := Send(context.Background(), env, Providers(), idx, s, "again", SendOptions{}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("refusal err = %v", err)
	}
	<-got
}

func TestPiWithoutExtensionUsesNewestSessionFile(t *testing.T) {
	h := newFakeHost(t)
	start := time.Now().Add(-time.Minute)
	h.procs[700] = Process{PID: 700, Command: "pi", Start: start}
	h.cwds[700] = "/work/q"
	old := h.write(".pi/agent/sessions/--work-q--/old.jsonl", jsonl(map[string]any{"type": "session", "id": "old"}))
	os.Chtimes(old, start.Add(-time.Hour), start.Add(-time.Hour))
	h.write(".pi/agent/sessions/--work-q--/new.jsonl", jsonl(map[string]any{"type": "session", "id": "new"}))

	idx := Discover(context.Background(), h.env(), []Provider{piProvider{}})
	if len(idx.Sessions) != 1 || idx.Sessions[0].NativeID != "new" || !strings.HasPrefix(idx.Sessions[0].Delivery, "unavailable") {
		t.Fatalf("sessions = %+v", idx.Sessions)
	}
}

func TestOpenCodeLiveRuleAndSend(t *testing.T) {
	h := newFakeHost(t)
	clientStart := time.Now().Add(-10 * time.Minute)
	h.procs[1] = Process{PID: 1, Command: "/Users/x/.opencode/bin/opencode serve --service"}
	h.procs[2] = Process{PID: 2, TTY: "/dev/ttys013", Command: "opencode", Start: clientStart}
	dir := t.TempDir()
	h.cwds[2] = dir
	ms := func(t time.Time) int64 { return t.UnixMilli() }
	h.respond = func(args []string) ([]byte, error) {
		path := args[3]
		switch {
		case path == "/api/session/active":
			return []byte(`{"data":{"ses_busy":{"type":"running"}}}`), nil
		case strings.HasPrefix(path, "/api/session?") && strings.Contains(path, "parentID=ses_viewed"):
			return []byte(fmt.Sprintf(`{"data":[{"id":"ses_child","parentID":"ses_viewed","time":{"created":%d,"updated":%d},"location":{"directory":%q}}]}`,
				ms(clientStart), ms(time.Now()), dir)), nil
		case strings.HasPrefix(path, "/api/session?") && strings.Contains(path, "parentID="):
			return []byte(`{"data":[]}`), nil
		case strings.HasPrefix(path, "/api/session?"):
			return []byte(fmt.Sprintf(`{"data":[
				{"id":"ses_viewed","title":"Viewed","time":{"created":1,"updated":%[1]d,"viewed":%[1]d},"location":{"directory":%[3]q}},
				{"id":"ses_stale","time":{"created":1,"updated":%[2]d,"viewed":%[2]d},"location":{"directory":%[3]q}},
				{"id":"ses_busy","time":{"created":1,"updated":1},"location":{"directory":"/elsewhere"}}
			]}`, ms(time.Now()), ms(clientStart.Add(-time.Hour)), dir)), nil
		case strings.HasSuffix(path, "/prompt"):
			return []byte(`{"data":{}}`), nil
		}
		return nil, fmt.Errorf("unexpected %v", args)
	}
	env := h.env()
	env.Getenv = func(key string) string {
		if key == "CONTEXT_DROP_OPENCODE_BIN" {
			return "opencode-test"
		}
		return ""
	}
	idx := Discover(context.Background(), env, []Provider{openCodeProvider{}})
	if len(idx.Warnings) > 0 {
		t.Fatal(idx.Warnings)
	}
	var ids []string
	for _, s := range idx.Sessions {
		ids = append(ids, s.NativeID+":"+s.Status)
	}
	if strings.Join(ids, ",") != "ses_busy:working,ses_viewed:idle,ses_child:idle" {
		t.Fatalf("live = %v", ids)
	}
	child, _ := idx.Resolve("ses_child")
	if _, err := Send(context.Background(), env, Providers(), idx, child, "check logs", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.commands[len(h.commands)-1], " "); got != `opencode-test api POST /api/session/ses_child/prompt -d {"text":"check logs"}` {
		t.Fatalf("prompt = %q", got)
	}
}

func TestOpenCodeNeverStartsTheService(t *testing.T) {
	h := newFakeHost(t)
	h.procs[2] = Process{PID: 2, Command: "opencode"}
	idx := Discover(context.Background(), h.env(), []Provider{openCodeProvider{}})
	if len(idx.Sessions) != 0 || len(h.commands) != 0 {
		t.Fatalf("sessions = %v commands = %v", idx.Sessions, h.commands)
	}
}

// scriptedProvider returns one transcript per Discover call.
type scriptedProvider struct {
	polls  *int
	frames [][]Message
}

func (p scriptedProvider) Kind() string { return Claude }
func (p scriptedProvider) Discover(context.Context, *Env) ([]Session, error) {
	*p.polls++
	return []Session{{ID: "cc-1", Agent: Claude, NativeID: "1", Status: StatusIdle}}, nil
}
func (p scriptedProvider) Transcript(context.Context, *Env, Session) ([]Message, error) {
	i := *p.polls - 1
	if i >= len(p.frames) {
		i = len(p.frames) - 1
	}
	return p.frames[i], nil
}
func (scriptedProvider) Send(context.Context, *Env, Session, string, SendOptions) error { return nil }

func TestWaitForReplyIgnoresTheTurnTheMessageWasQueuedBehind(t *testing.T) {
	sent := time.Now()
	earlier := []Message{
		{Role: "user", Text: "refactor auth", Time: sent.Add(-time.Minute)},
		{Role: "assistant", Text: "Refactor finished.", Time: sent.Add(time.Second)}, // the busy turn ends after the send
	}
	withReply := append(append([]Message{}, earlier...),
		Message{Role: "user", Text: "[from context-drop] now   run\nthe tests", Time: sent.Add(2 * time.Second)},
		Message{Role: "assistant", Text: "Tests pass.", Time: sent.Add(3 * time.Second)},
	)
	polls := 0
	p := scriptedProvider{polls: &polls, frames: [][]Message{earlier, earlier, withReply}}
	msgs, _, err := WaitForReply(context.Background(), func() (*Env, error) { return &Env{}, nil }, []Provider{p},
		Session{ID: "cc-1", Agent: Claude}, WaitOptions{Sent: "now run the tests", Since: sent.Add(-5 * time.Second), Interval: time.Millisecond, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if polls != 3 || len(msgs) != 2 || msgs[1].Text != "Tests pass." {
		t.Fatalf("polls=%d msgs=%+v", polls, msgs)
	}
}

func TestPiIgnoresRegistryOfReusedPID(t *testing.T) {
	h := newFakeHost(t)
	h.procs[555] = Process{PID: 555, Command: "/usr/sbin/cron", Start: time.Now()}
	reg, _ := json.Marshal(piRegistry{PID: 555, SessionID: "stale", Socket: "/tmp/none.sock", UpdatedAt: time.Now().Add(-24 * time.Hour).UnixMilli()})
	h.write(".context-drop/pi/555.json", string(reg))
	if idx := Discover(context.Background(), h.env(), []Provider{piProvider{}}); len(idx.Sessions) != 0 {
		t.Fatalf("sessions = %+v", idx.Sessions)
	}
}

func TestWaitForReplyIgnoresAnEarlierIdenticalPrompt(t *testing.T) {
	// Transcripts without timestamps cannot be filtered by time, so the
	// pre-send match count is what separates the old "continue" from the new one.
	before := []Message{{Role: "user", Text: "continue"}, {Role: "assistant", Text: "old answer"}}
	after := append(append([]Message{}, before...), Message{Role: "user", Text: "continue"}, Message{Role: "assistant", Text: "new answer"})
	polls := 0
	p := scriptedProvider{polls: &polls, frames: [][]Message{before, before, after}}
	msgs, _, err := WaitForReply(context.Background(), func() (*Env, error) { return &Env{}, nil }, []Provider{p},
		Session{ID: "cc-1", Agent: Claude}, WaitOptions{Sent: "continue", PriorMatches: SentMatches(before, "continue", time.Time{}),
			Interval: time.Millisecond, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if polls != 3 || len(msgs) != 2 || msgs[1].Text != "new answer" {
		t.Fatalf("polls=%d msgs=%+v", polls, msgs)
	}
}
