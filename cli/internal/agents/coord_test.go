package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
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
		if _, ok := env.takeLaunch(context.Background(), Session{ID: fmt.Sprint("s", i), PID: i}); !ok {
			t.Fatalf("record %d was lost", i)
		}
	}
}

func TestSelfUnderOpenCodeIsEveryOpenCodeSession(t *testing.T) {
	h := newFakeHost(t)
	h.procs[500] = Process{PID: 500, PPID: 90}
	h.procs[90] = Process{PID: 90, PPID: 1, Command: "/opt/bin/opencode"}
	idx := Index{Sessions: []Session{{ID: "oc-1", Agent: OpenCode}, {ID: "oc-2", Agent: OpenCode}, {ID: "cc-1", Agent: Claude, PID: 7}}}
	found, err := SelfCandidates(context.Background(), h.env(), idx, 500)
	if err != nil || len(found) != 2 || found[0].ID != "oc-1" || found[1].ID != "oc-2" {
		t.Fatalf("candidates = %+v, %v", found, err)
	}
}

func TestStopFindsTheRecordOfARenamedPiSession(t *testing.T) {
	h := muxHost(t) // pid 10 runs in tmux pane %3
	inner := h.respond
	h.respond = func(args []string) ([]byte, error) {
		if args[0] == "tmux" && args[1] == "kill-pane" {
			return nil, nil
		}
		return inner(args)
	}
	env := h.env()
	env.Signal = func(int, syscall.Signal) error { return nil }
	// Another kind of session sharing the process is not a rename.
	if err := env.recordLaunch(Session{ID: "cx-a", Agent: Codex, PID: 10}, Location{Mux: MuxTmux, Pane: "%3"}); err != nil {
		t.Fatal(err)
	}
	if how, _ := Stop(context.Background(), env, Session{ID: "cx-b", Agent: Codex, PID: 10, TTY: "/dev/ttys010"}); !strings.HasPrefix(how, "sent SIGTERM") {
		t.Fatalf("closed another session's pane: %q", how)
	}
	if err := env.recordLaunch(Session{ID: "pi-pid-10", Agent: Pi, PID: 10}, Location{Mux: MuxTmux, Pane: "%3"}); err != nil {
		t.Fatal(err)
	}
	how, err := Stop(context.Background(), h.env(), Session{ID: "pi-file-id", Agent: Pi, PID: 10, TTY: "/dev/ttys010"})
	if err != nil || how != "closed tmux pane %3" {
		t.Fatalf("stop = %q, %v", how, err)
	}
}
