package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
)

type fakeDelegationRuntime struct {
	mu        sync.Mutex
	reports   []runtimeclient.ParentReport
	claimed   map[string]bool
	tasks     []runtimeclient.Delegation
	delegated string
}

func (f *fakeDelegationRuntime) Delegate(_ context.Context, task, _ string) (runtimeclient.Run, error) {
	f.delegated = task
	return runtimeclient.Run{ID: "run_cont", Backend: "herdr", Status: "running"}, nil
}
func (f *fakeDelegationRuntime) DelegateCapability(context.Context) (string, error) {
	return "cap", nil
}
func (f *fakeDelegationRuntime) PendingReports(context.Context) ([]runtimeclient.ParentReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runtimeclient.ParentReport
	for _, r := range f.reports {
		if !f.claimed[r.ID] {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeDelegationRuntime) DeliverReport(_ context.Context, id string) (runtimeclient.ParentReport, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	if f.claimed[id] {
		return runtimeclient.ParentReport{}, false, nil
	}
	for _, r := range f.reports {
		if r.ID == id {
			f.claimed[id] = true
			return r, true, nil
		}
	}
	return runtimeclient.ParentReport{}, false, nil
}
func (f *fakeDelegationRuntime) Delegations(context.Context) ([]runtimeclient.Delegation, error) {
	return f.tasks, nil
}

type reportCommander struct{ sends []string }

func (c *reportCommander) Run(_ context.Context, _ string, args []string, _ int) (imessage.CommandResult, error) {
	if len(args) > 0 && args[0] == "send" {
		c.sends = append(c.sends, args[4])
	}
	return imessage.CommandResult{Stdout: []byte(`{"ok":true}`)}, nil
}

func TestReportDeliveryIsIdempotentAcrossRunnerRestart(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "r1", RunID: "run_123", Kind: "failed", Message: "payment confirmation was not provided"}}, claimed: map[string]bool{}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.ChatID = "chat"
	cfg.ImsgPath = "/bin/echo"
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	runner.deliverReportsOnce(context.Background())
	restarted := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	restarted.deliverReportsOnce(context.Background())
	if len(commander.sends) != 1 {
		t.Fatalf("sends=%#v", commander.sends)
	}
	if !strings.Contains(commander.sends[0], "failed") || !strings.Contains(commander.sends[0], "payment confirmation") {
		t.Fatalf("notification=%q", commander.sends[0])
	}
}

type delegatingResponder struct{ backend *fakeDelegationRuntime }

func (r *delegatingResponder) Prepare(context.Context) (imessage.PersistentResponderState, error) {
	return imessage.PersistentResponderState{}, nil
}
func (r *delegatingResponder) Respond(ctx context.Context, prompt string, _ int) (imessage.Response, error) {
	run, err := r.backend.Delegate(ctx, prompt, "chat")
	if err != nil {
		return imessage.Response{}, err
	}
	r.backend.reports = append(r.backend.reports, runtimeclient.ParentReport{ID: "report-e2e", RunID: run.ID, Kind: "completed", Message: "local fixture finished"})
	return imessage.Response{Reply: "worker " + run.ID + " started visibly"}, nil
}
func (*delegatingResponder) Close() error { return nil }

func TestIncomingActionableDelegatesAndDeliversReportE2E(t *testing.T) {
	backend := &fakeDelegationRuntime{claimed: map[string]bool{}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.Trusted = true
	cfg.ChatID = "chat"
	cfg.ImsgPath = "/bin/echo"
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	now := time.Now().UTC()
	if err := store.Update(func(st *orchestrator.State) error {
		st.MessageJobs["100"] = orchestrator.MessageJob{MessageID: "100", Status: "queued", ClaimedAt: now, UpdatedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: &delegatingResponder{backend: backend}}}
	runner.processMessage(context.Background(), imessage.Message{ID: "100", Text: "handle this actionable request", ChatID: "chat"})
	runner.deliverReportsOnce(context.Background())
	if len(commander.sends) != 2 {
		t.Fatalf("sends=%#v", commander.sends)
	}
	if !strings.Contains(commander.sends[0], "run_cont") || !strings.Contains(commander.sends[1], "local fixture finished") {
		t.Fatalf("unexpected e2e sends=%#v", commander.sends)
	}
	if !strings.Contains(backend.delegated, "actionable request") {
		t.Fatalf("delegate task=%q", backend.delegated)
	}
}

func TestActiveTaskUsesNewWorkerContinuationFallback(t *testing.T) {
	backend := &fakeDelegationRuntime{tasks: []runtimeclient.Delegation{{RunID: "run_old", ChatID: "chat", Task: "book a tee time", Status: "running", CreatedAt: "2026-01-01T00:00:00Z"}}}
	cfg := imessage.Defaults()
	cfg.ChatID = "chat"
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg}}
	reply, handled := runner.continueActiveTask(context.Background(), "do it after 10am")
	if !handled || !strings.Contains(reply, "run_cont") {
		t.Fatalf("handled=%v reply=%q", handled, reply)
	}
	if !strings.Contains(backend.delegated, "book a tee time") || !strings.Contains(backend.delegated, "after 10am") {
		t.Fatalf("delegated=%q", backend.delegated)
	}
}
