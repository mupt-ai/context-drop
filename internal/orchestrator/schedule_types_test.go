package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMigratesLegacyScheduleWithoutDroppingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := map[string]any{"schedules": []map[string]any{{
		"name": "legacy", "agent": "pi", "repo": dir, "prompt": "keep me", "every": int64(time.Minute), "enabled": true,
	}}, "jobs": []any{}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Schedules) != 1 || st.Schedules[0].Type != ScheduleAgent || st.Schedules[0].Overlap != OverlapSkip || st.Schedules[0].Prompt != "keep me" || st.Schedules[0].MissedRunPolicy != "latest" {
		t.Fatalf("migration lost data: %#v", st.Schedules)
	}
}

func TestLoadTreatsLegacyLaunchJobsAsTerminalHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := map[string]any{
		"schedules": []map[string]any{{
			"name": "legacy", "agent": "pi", "repo": dir, "prompt": "keep me", "every": int64(time.Minute), "enabled": true,
		}},
		"jobs": []map[string]any{{
			"id": "job_old", "schedule_name": "legacy", "outcome": "launched", "runtime_run_id": "run_old", "created_at": time.Now().Add(-time.Hour),
		}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Jobs) != 1 || st.Jobs[0].Status != "completed" {
		t.Fatalf("legacy launch must be terminal history: %#v", st.Jobs)
	}
	if hasActiveJob(st, "legacy") {
		t.Fatal("legacy launch blocks future overlap-safe occurrences")
	}
}

func TestClaimDueSkipsActiveOverlapAndAdvancesOccurrence(t *testing.T) {
	now := time.Now().UTC()
	st := State{}
	s := Schedule{Name: "one", Type: ScheduleCommand, Command: []string{"/bin/echo", "ok"}, Cwd: t.TempDir(), Every: time.Minute, Enabled: true, Overlap: OverlapSkip, MissedRunPolicy: "latest"}
	if err := Upsert(&st, s, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	first := ClaimDue(&st, now)
	if len(first) != 1 {
		t.Fatalf("first claims=%d", len(first))
	}
	st.Schedules[0].NextRunAt = now.Add(time.Minute)
	now = now.Add(time.Minute)
	second := ClaimDue(&st, now)
	if len(second) != 0 {
		t.Fatalf("overlap claim launched: %#v", second)
	}
	if len(st.Jobs) != 2 || st.Jobs[1].Status != "skipped" || st.Jobs[0].OccurrenceKey == st.Jobs[1].OccurrenceKey {
		t.Fatalf("jobs=%#v", st.Jobs)
	}
}

func TestValidateTypedSchedules(t *testing.T) {
	base := Schedule{Name: "x", Every: time.Minute, Enabled: true, Overlap: OverlapSkip, MissedRunPolicy: "latest"}
	command := base
	command.Type = ScheduleCommand
	command.Command = []string{"/bin/echo", "a;b"}
	command.Cwd = t.TempDir()
	if err := ValidateSchedule(command); err != nil {
		t.Fatal(err)
	}
	watch := base
	watch.Type = ScheduleWatch
	watch.Backend = "herdr"
	watch.WatchPane = "workspace:pane"
	if err := ValidateSchedule(watch); err != nil {
		t.Fatal(err)
	}
	command.Overlap = "replace"
	if err := ValidateSchedule(command); err == nil {
		t.Fatal("replace should be rejected until safely implemented")
	}
}
