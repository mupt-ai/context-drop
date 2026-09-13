package daemon

import (
	"context"
	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentSchedulesClaimSharedWorkerPool(t *testing.T) {
	for _, kind := range []string{orchestrator.ScheduleAgent, orchestrator.ScheduleWatch} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			store := orchestrator.Store{Path: filepath.Join(dir, "state.json")}
			now := time.Now().UTC()
			s := orchestrator.Schedule{Name: "scheduled", Type: kind, Agent: "pi", Repo: dir, Cwd: dir, Prompt: "inspect", Command: []string{"/usr/bin/true"}, Backend: "herdr", WatchPane: "w1:p1", Every: time.Minute, Enabled: true, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest"}
			if err := store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, now.Add(-time.Minute)) }); err != nil {
				t.Fatal(err)
			}
			runtime := &fakeRuntime{}
			cfg := imessage.Defaults()
			cfg.Enabled = true
			cfg.RouterMode = true
			cfg.ChatID = "chat"
			r := Runner{Store: store, Runtime: runtime, IMessage: &imessage.Adapter{Config: cfg}, Now: func() time.Time { return now }}
			if err := r.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			st, _ := store.Load()
			if len(st.Jobs) != 1 || st.Jobs[0].Status != "running" || st.Jobs[0].RuntimeRunID == "" {
				t.Fatalf("jobs=%+v", st.Jobs)
			}
		})
	}
}
