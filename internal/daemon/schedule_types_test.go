package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
)

func TestCommandScheduleUsesExactArgvWithoutShell(t *testing.T) {
	dir := t.TempDir()
	store := orchestrator.Store{Path: filepath.Join(dir, "state.json")}
	now := time.Now().UTC()
	s := orchestrator.Schedule{Name: "command", Type: orchestrator.ScheduleCommand, Command: []string{"/bin/echo", "hello; touch injected"}, Cwd: dir, Every: time.Minute, Enabled: true, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest"}
	if err := store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, now.Add(-time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	r := Runner{Store: store, Runtime: &fakeRuntime{}, Notifier: &fakeNotifier{}, Now: func() time.Time { return now }}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Fatalf("shell interpolation occurred: %v", err)
	}
	st, _ := store.Load()
	if len(st.Jobs) != 1 || st.Jobs[0].Status != "completed" {
		t.Fatalf("jobs=%#v", st.Jobs)
	}
}

func TestCommandScheduleTimesOut(t *testing.T) {
	dir := t.TempDir()
	store := orchestrator.Store{Path: filepath.Join(dir, "state.json")}
	now := time.Now().UTC()
	s := orchestrator.Schedule{Name: "timeout", Type: orchestrator.ScheduleCommand, Command: []string{"/bin/sleep", "1"}, Cwd: dir, Every: time.Minute, Enabled: true, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest", Timeout: 10 * time.Millisecond}
	_ = store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, now.Add(-time.Minute)) })
	r := Runner{Store: store, Runtime: &fakeRuntime{}, Notifier: &fakeNotifier{}, Now: func() time.Time { return now }}
	_ = r.Tick(context.Background())
	st, _ := store.Load()
	if st.Jobs[0].Status != "timed_out" {
		t.Fatalf("job=%#v", st.Jobs[0])
	}
}

func TestCommandRetriesAndAutoPauses(t *testing.T) {
	dir := t.TempDir()
	store := orchestrator.Store{Path: filepath.Join(dir, "state.json")}
	now := time.Now().UTC()
	s := orchestrator.Schedule{Name: "fail", Type: orchestrator.ScheduleCommand, Command: []string{"/usr/bin/false"}, Cwd: dir, Every: time.Minute, Enabled: true, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest", MaxRetries: 2, AutoPauseAfter: 1}
	_ = store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, now.Add(-time.Minute)) })
	r := Runner{Store: store, Runtime: &fakeRuntime{}, Notifier: &fakeNotifier{}, Now: func() time.Time { return now }}
	_ = r.Tick(context.Background())
	st, _ := store.Load()
	if st.Jobs[0].Attempt != 3 || st.Schedules[0].Enabled || st.Schedules[0].ConsecutiveFailures != 1 {
		t.Fatalf("state=%#v jobs=%#v", st.Schedules, st.Jobs)
	}
}

func TestWatchOnlyNotifiesTerminalStateChanges(t *testing.T) {
	dir := t.TempDir()
	store := orchestrator.Store{Path: filepath.Join(dir, "state.json")}
	now := time.Now().UTC()
	notifier := &fakeNotifier{}
	s := orchestrator.Schedule{Name: "watch", Type: orchestrator.ScheduleWatch, Backend: "herdr", WatchPane: "p1", Every: time.Minute, Enabled: true, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest"}
	_ = store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, now.Add(-time.Minute)) })
	r := Runner{Store: store, Notifier: notifier, Now: func() time.Time { return now }}
	claim := func() orchestrator.Claim {
		var c orchestrator.Claim
		_ = store.Update(func(st *orchestrator.State) error {
			st.Schedules[0].NextRunAt = now
			c = orchestrator.ClaimDue(st, now)[0]
			return nil
		})
		return c
	}
	r.ExecuteClaim(context.Background(), claim(), []runtimeclient.ManagedTask{{PaneID: "p1", Status: "blocked"}}, now)
	r.ExecuteClaim(context.Background(), claim(), []runtimeclient.ManagedTask{{PaneID: "p1", Status: "blocked"}}, now)
	if len(notifier.messages) != 1 {
		t.Fatalf("notifications=%#v", notifier.messages)
	}
}
