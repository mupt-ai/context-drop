package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSelfFindsTheNearestAgentAncestor(t *testing.T) {
	h := newFakeHost(t)
	// context-drop (500) runs in a shell (400) that the claude session (300) started.
	h.procs[500] = Process{PID: 500, PPID: 400}
	h.procs[400] = Process{PID: 400, PPID: 300}
	h.procs[300] = Process{PID: 300, PPID: 1}
	// A shell under a Codex app-server that holds two threads.
	h.procs[600] = Process{PID: 600, PPID: 100}
	h.procs[100] = Process{PID: 100, PPID: 1}
	h.procs[700] = Process{PID: 700, PPID: 1}
	idx := Index{Sessions: []Session{
		{ID: "cc-a", PID: 300}, {ID: "cc-a-sub", PID: 300, Subagent: true},
		{ID: "cx-1", PID: 100}, {ID: "cx-2", PID: 100},
	}}
	env := h.env()
	if s, err := Self(context.Background(), env, idx, 500); err != nil || s.ID != "cc-a" {
		t.Fatalf("self = %q, %v", s.ID, err)
	}
	if _, err := Self(context.Background(), env, idx, 600); err == nil || !strings.Contains(err.Error(), "--as") {
		t.Fatalf("shared process: err = %v", err)
	}
	if found, _ := SelfCandidates(context.Background(), env, idx, 600); len(found) != 2 {
		t.Fatalf("candidates under a shared process = %+v", found)
	}
	if _, err := Self(context.Background(), env, idx, 700); !errors.Is(err, ErrNoSelf) {
		t.Fatalf("outside any session: err = %v", err)
	}
}

func TestChanges(t *testing.T) {
	now := time.Now()
	prev := []Session{{ID: "a", Status: StatusIdle}, {ID: "b", Status: StatusWorking}, {ID: "c", Status: StatusIdle}}
	next := []Session{{ID: "a", Status: StatusWorking}, {ID: "c", Status: StatusIdle}, {ID: "d", Status: StatusIdle}}
	var got []string
	for _, c := range Changes(prev, next, now) {
		got = append(got, c.Event+" "+c.ID+" "+c.From+">"+c.To)
	}
	want := "ended b working>|started d >idle|status a idle>working"
	if strings.Join(got, "|") != want {
		t.Fatalf("changes = %q, want %q", strings.Join(got, "|"), want)
	}
	if len(Changes(next, next, now)) != 0 {
		t.Fatal("no change reported changes")
	}
}

// gitRepo makes a repository at dir/main with a linked worktree at
// dir/feature, laid out the way git lays them out.
func gitRepo(t *testing.T) (main, feature string) {
	t.Helper()
	dir := t.TempDir()
	main, feature = filepath.Join(dir, "main"), filepath.Join(dir, "feature")
	gitdir := filepath.Join(main, ".git", "worktrees", "feature")
	for _, d := range []string{gitdir, filepath.Join(main, "src"), filepath.Join(feature, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(feature, ".git"), "gitdir: "+gitdir+"\n")
	write(filepath.Join(gitdir, "commondir"), "../..\n")
	return canonicalPath(main), canonicalPath(feature)
}

func TestClaims(t *testing.T) {
	main, feature := gitRepo(t)
	h := newFakeHost(t)
	env := h.env()
	store := env.Claims()
	a, b := Session{ID: "cc-a", Agent: Claude}, Session{ID: "cx-b", Agent: Codex}
	idx := Index{Sessions: []Session{a, b}}
	now := time.Now()

	if err := store.Add(idx, []Claim{NewClaim(a, "src", main, "refactor", now), NewClaim(a, "Release notes", main, "", now)}, false); err != nil {
		t.Fatal(err)
	}
	// The same directory in another worktree of the repository overlaps.
	err := store.Add(idx, []Claim{NewClaim(b, "src/api.go", feature, "", now)}, false)
	var conflict ClaimConflict
	if !errors.As(err, &conflict) || conflict.Held.Session != "cc-a" {
		t.Fatalf("overlapping path accepted: %v", err)
	}
	if err := store.Add(idx, []Claim{NewClaim(b, "release NOTES", main, "", now)}, false); err == nil {
		t.Fatal("same task name accepted")
	}
	if err := store.Add(idx, []Claim{NewClaim(b, "docs", main, "", now)}, false); err != nil {
		t.Fatalf("a separate path was refused: %v", err)
	}
	if err := store.Add(idx, []Claim{NewClaim(b, "src/api.go", feature, "", now)}, true); err != nil {
		t.Fatalf("--force: %v", err)
	}
	live, err := store.Live(idx)
	if err != nil || len(live) != 4 {
		t.Fatalf("live = %+v, %v", live, err)
	}
	if live[0].Path != filepath.Join(main, "src") || live[0].RepoPath != "src" || live[1].Path != "" {
		t.Fatalf("path and task claims = %+v", live[:2])
	}

	released, err := store.Release(idx, "cc-a", []Claim{NewClaim(a, "./src", main, "", time.Time{})})
	if err != nil || len(released) != 1 || released[0].Target != "src" {
		t.Fatalf("released = %+v, %v", released, err)
	}
	// When b's session ends, its claims go with it.
	live, err = store.Live(Index{Sessions: []Session{a}})
	if err != nil || len(live) != 1 || live[0].Target != "Release notes" {
		t.Fatalf("after b ended = %+v, %v", live, err)
	}
	if released, _ := store.Release(Index{Sessions: []Session{a}}, "cc-a", nil); len(released) != 1 {
		t.Fatalf("release all = %+v", released)
	}
}

func TestEditedFiles(t *testing.T) {
	h := newFakeHost(t)
	old, recent := "2026-11-11T08:00:00Z", "2026-11-11T12:00:00Z"
	since := parseTime("2026-11-11T10:00:00Z")
	claude := h.write("claude.jsonl", jsonl(
		map[string]any{"type": "assistant", "timestamp": old, "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/w/old.go"}}}}},
		map[string]any{"type": "assistant", "timestamp": recent, "message": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "editing"},
			map[string]any{"type": "tool_use", "name": "Write", "input": map[string]any{"file_path": "/w/new.go"}},
			map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/w/read.go"}}}}},
	))
	codex := h.write("codex.jsonl", jsonl(
		map[string]any{"type": "response_item", "timestamp": recent, "payload": map[string]any{"type": "custom_tool_call", "name": "apply_patch",
			"input": "*** Begin Patch\n*** Update File: cli/a.go\n@@\n*** Add File: /abs/b.go\n*** End Patch"}},
		map[string]any{"type": "response_item", "timestamp": recent, "payload": map[string]any{"type": "function_call", "name": "apply_patch",
			"arguments": `{"input":"*** Begin Patch\n*** Delete File: c.go\n*** End Patch"}`}},
	))
	pi := h.write("pi.jsonl", jsonl(
		map[string]any{"type": "message", "timestamp": recent, "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "toolCall", "name": "edit", "arguments": map[string]any{"path": "p.go"}}}}},
	))
	cases := []struct {
		s    Session
		want string
	}{
		{Session{Agent: Claude, Transcript: claude, Cwd: "/w"}, "/w/new.go"},
		{Session{Agent: Codex, Transcript: codex, Cwd: "/repo"}, "/repo/cli/a.go /abs/b.go /repo/c.go"},
		{Session{Agent: Pi, Transcript: pi, Cwd: "/p"}, "/p/p.go"},
		{Session{Agent: OpenCode, Transcript: pi, Cwd: "/p"}, ""},
	}
	for _, c := range cases {
		var got []string
		for _, e := range EditedFiles(c.s, since) {
			got = append(got, e.Path)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s edits = %q, want %q", c.s.Agent, got, c.want)
		}
	}
}

func TestFindOverlaps(t *testing.T) {
	main, feature := gitRepo(t)
	h := newFakeHost(t)
	edit := func(name, path string) string {
		return h.write(name, jsonl(map[string]any{"type": "assistant", "timestamp": time.Now().UTC().Format(time.RFC3339),
			"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": path}}}}}))
	}
	sessions := []Session{
		{ID: "cc-a", Agent: Claude, Cwd: main, Transcript: edit("a.jsonl", filepath.Join(main, "src/api.go"))},
		{ID: "cc-b", Agent: Claude, Cwd: filepath.Join(main, "src"), Transcript: edit("b.jsonl", filepath.Join(main, "README.md"))},
		{ID: "cc-c", Agent: Claude, Cwd: feature, Transcript: edit("c.jsonl", filepath.Join(feature, "src/api.go"))},
		{ID: "cc-d", Agent: Claude, Cwd: t.TempDir()}, // outside any repository
		{ID: "cc-a-sub", Agent: Claude, Cwd: main, Subagent: true},
	}
	claims := []Claim{NewClaim(Session{ID: "cc-b"}, "src", main, "", time.Now())}
	var got []string
	for _, o := range FindOverlaps(sessions, claims, time.Now().Add(-time.Hour)) {
		got = append(got, o.Kind+" "+strings.TrimPrefix(o.Path, main)+" "+strings.Join(o.Sessions, ","))
	}
	want := []string{
		"claim /src cc-b,cc-a,cc-c",
		"file src/api.go cc-a,cc-c", // the same file in two worktrees
		"worktree  cc-a,cc-b",       // main is shared by a and b; the subagent does not count
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("overlaps =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStop(t *testing.T) {
	h := muxHost(t) // pid 10 is in tmux pane %3
	h.procs[10] = Process{PID: 10, TTY: "/dev/ttys010", Command: "claude"}
	h.procs[40] = Process{PID: 40, Command: "codex app-server --listen ws://127.0.0.1:1"}
	inner := h.respond
	h.respond = func(args []string) ([]byte, error) {
		if args[0] == "tmux" && args[1] == "kill-pane" {
			return nil, nil
		}
		return inner(args)
	}
	var signalled []string
	env := h.env()
	env.Signal = func(pid int, sig syscall.Signal) error {
		signalled = append(signalled, fmt.Sprint(sig, pid))
		return nil
	}
	ctx := context.Background()
	launched := Session{ID: "cc-launched", PID: 10, TTY: "/dev/ttys010"}

	// A pane the user opened keeps its shell: the agent gets SIGTERM.
	how, err := Stop(ctx, env, launched)
	if err != nil || !strings.HasPrefix(how, "sent SIGTERM to claude") || len(signalled) != 1 {
		t.Fatalf("stop = %q, %v, signals %q", how, err, signalled)
	}
	// A pane `start` opened for this session is closed instead.
	if err := env.recordLaunch(launched, Location{Mux: MuxTmux, Pane: "%3"}); err != nil {
		t.Fatal(err)
	}
	how, err = Stop(ctx, h.env(), launched)
	if err != nil || how != "closed tmux pane %3" || strings.Join(h.commands[len(h.commands)-1], " ") != "tmux kill-pane -t %3" {
		t.Fatalf("stop = %q, %v, last %q", how, err, h.commands[len(h.commands)-1])
	}
	// The record is used once: the pane is gone, so a second stop signals.
	if how, _ := Stop(ctx, env, launched); !strings.HasPrefix(how, "sent SIGTERM") {
		t.Fatalf("second stop = %q", how)
	}
	// The record is per session: another session in the same pane is not ours.
	if how, _ := Stop(ctx, env, Session{ID: "cc-other", PID: 10, TTY: "/dev/ttys010"}); !strings.HasPrefix(how, "sent SIGTERM") {
		t.Fatalf("another session's pane was closed: %q", how)
	}
	if _, err := Stop(ctx, env, Session{ID: "cx-1", PID: 40}); err == nil || !strings.Contains(err.Error(), "shared process") {
		t.Fatalf("shared app-server: %v", err)
	}
	// A thread `start` opened under a shared app-server is stopped by its pane.
	codex := Session{ID: "cx-2", PID: 40}
	if err := env.recordLaunch(codex, Location{Mux: MuxTmux, Pane: "%8"}); err != nil {
		t.Fatal(err)
	}
	if how, err := Stop(ctx, env, codex); err != nil || how != "closed tmux pane %8" {
		t.Fatalf("recorded codex pane: %q, %v", how, err)
	}
	// After a restart the same session has a new process, so an old record does not apply.
	if err := env.recordLaunch(Session{ID: "cc-old", PID: 77}, Location{Mux: MuxTmux, Pane: "%3"}); err != nil {
		t.Fatal(err)
	}
	if how, _ := Stop(ctx, env, Session{ID: "cc-old", PID: 10, TTY: "/dev/ttys010"}); !strings.HasPrefix(how, "sent SIGTERM") {
		t.Fatalf("stale record closed a pane: %q", how)
	}
	if _, err := Stop(ctx, env, Session{ID: "cc-sub", PID: 10, Subagent: true, ParentID: "cc-launched"}); err == nil {
		t.Fatal("stopped a subagent on its own")
	}
	if _, err := Stop(ctx, env, Session{ID: "gone", PID: 99}); err == nil {
		t.Fatal("stopped a session with no process")
	}
}

func TestClaimsSurviveAPartialDiscovery(t *testing.T) {
	main, feature := gitRepo(t)
	h := newFakeHost(t)
	h.procs[300] = Process{PID: 300}
	h.procs[400] = Process{PID: 400}
	env := h.env()
	store := env.Claims()
	a, b := Session{ID: "cc-a", PID: 300}, Session{ID: "cc-b", PID: 400}
	if err := store.Add(Index{Sessions: []Session{a}}, []Claim{NewClaim(a, "src", main, "", time.Now())}, false); err != nil {
		t.Fatal(err)
	}
	// A pass that missed a's session hides its claim but keeps it.
	if live, _ := store.Live(Index{}); len(live) != 0 {
		t.Fatalf("live = %+v", live)
	}
	if err := store.Add(Index{Sessions: []Session{b}}, []Claim{NewClaim(b, "src", feature, "", time.Now())}, false); err == nil {
		t.Fatal("a claim was lost to a discovery pass that missed its session")
	}
	// A name one agent saw as a task and another as a path still conflicts.
	if err := store.Add(Index{Sessions: []Session{a}}, []Claim{NewClaim(a, "docs", main, "", time.Now())}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(feature, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Index{Sessions: []Session{b}}, []Claim{NewClaim(b, "docs", feature, "", time.Now())}, false); err == nil {
		t.Fatal("task and path claims on the same name did not conflict")
	}
	// Releasing from another worktree of the repository matches.
	released, err := store.Release(Index{Sessions: []Session{a}}, "cc-a", []Claim{NewClaim(a, "src", feature, "", time.Time{})})
	if err != nil || len(released) != 1 {
		t.Fatalf("released = %+v, %v", released, err)
	}
	// Once a's process exits, its claims go.
	delete(h.procs, 300)
	env = h.env()
	if err := env.Claims().Add(Index{Sessions: []Session{b}}, []Claim{NewClaim(b, "docs", feature, "", time.Now())}, false); err != nil {
		t.Fatalf("a dead session's claim still blocks: %v", err)
	}
}

func TestLaunchRecordsSurviveConcurrentStarts(t *testing.T) {
	env := newFakeHost(t).env()
	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = env.recordLaunch(Session{ID: fmt.Sprint("s", i), PID: i}, Location{Mux: MuxTmux, Pane: fmt.Sprint("%", i)})
		}(i)
	}
	wg.Wait()
	for i := 1; i <= 20; i++ {
		if _, ok := env.takeLaunch(Session{ID: fmt.Sprint("s", i), PID: i}); !ok {
			t.Fatalf("record %d was lost", i)
		}
	}
}
