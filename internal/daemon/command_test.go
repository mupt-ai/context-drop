package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/orchestrator"
)

func TestCommandScheduleBypassesAgents(t *testing.T) {
	for _, tc := range []struct {
		name, script, status string
		timeout              time.Duration
	}{
		{"success", `printf '%s' "$1"; pwd`, "completed", time.Second},
		{"failure", `exit 7`, "failed", time.Second},
		{"timeout", `sleep 30`, "timed_out", 20 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := orchestrator.Store{Path: filepath.Join(dir, "state.json")}
			now := time.Now().UTC()
			s := orchestrator.Schedule{Name: "script", Type: orchestrator.ScheduleCommand, Cwd: dir, Command: []string{"/bin/sh", "-c", tc.script, "test", "literal $(touch unwanted)"}, Every: time.Minute, Enabled: true, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest", Timeout: tc.timeout}
			if err := store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, now.Add(-time.Minute)) }); err != nil {
				t.Fatal(err)
			}
			// No runtime or messaging adapter: scripts must work independently of both.
			r := Runner{Store: store, Now: func() time.Time { return now }}
			if err := r.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			r.commandWorkers.Wait()
			st, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Jobs) != 1 || st.Jobs[0].Status != tc.status || !strings.HasPrefix(st.Jobs[0].RuntimeRunID, "local:") {
				t.Fatalf("jobs=%+v", st.Jobs)
			}
			if tc.name == "success" {
				data, err := os.ReadFile(filepath.Join(dir, "command-logs", st.Jobs[0].ID+".log"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), "literal $(touch unwanted)") || !strings.Contains(string(data), dir) {
					t.Fatalf("output=%s", data)
				}
				if _, err := os.Stat(filepath.Join(dir, "unwanted")); !os.IsNotExist(err) {
					t.Fatal("argv was interpreted as shell syntax")
				}
			}
		})
	}
}
