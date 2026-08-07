package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testSchedule(t *testing.T, name string) Schedule {
	t.Helper()
	repo := t.TempDir()
	return Schedule{Name: name, Agent: "mock", Repo: repo, Prompt: "do work", Every: time.Minute, Enabled: true}
}

func TestUpsertAndDueClaim(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := State{}
	s := testSchedule(t, "daily")
	if err := Upsert(&st, s, now); err != nil {
		t.Fatal(err)
	}
	if got := len(Due(&st, now)); got != 0 {
		t.Fatalf("due immediately = %d", got)
	}
	when := now.Add(time.Minute)
	due := Due(&st, when)
	if len(due) != 1 || due[0].Name != "daily" {
		t.Fatalf("due = %#v", due)
	}
	if got := len(Due(&st, when)); got != 0 {
		t.Fatalf("same occurrence claimed twice: %d", got)
	}
	if st.Schedules[0].LastFiredAt == nil || !st.Schedules[0].NextRunAt.Equal(when.Add(time.Minute)) {
		t.Fatalf("claim not persisted: %#v", st.Schedules[0])
	}
}

func TestValidateSchedule(t *testing.T) {
	s := testSchedule(t, "ok")
	s.Prompt = string(make([]byte, MaxPromptBytes+1))
	if err := ValidateSchedule(s); err == nil {
		t.Fatal("expected prompt length error")
	}
	s = testSchedule(t, "ok")
	s.Every = time.Second
	if err := ValidateSchedule(s); err == nil {
		t.Fatal("expected interval error")
	}
	s = testSchedule(t, "ok")
	s.Repo = "."
	if err := ValidateSchedule(s); err == nil {
		t.Fatal("expected absolute repo error")
	}
}

func TestStorePermissionsRetentionAndSeenBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := Store{Path: path}
	st := State{SeenHandoffIDs: map[string]string{}}
	for i := 0; i < MaxJobs+20; i++ {
		st.Jobs = append(st.Jobs, Job{ID: fmt.Sprint(i)})
	}
	for i := 0; i < MaxSeenHandoffs+20; i++ {
		st.SeenHandoffIDs[fmt.Sprint(i)] = time.Unix(int64(i), 0).UTC().Format(time.RFC3339Nano)
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Jobs) != MaxJobs {
		t.Fatalf("jobs = %d", len(loaded.Jobs))
	}
	if len(loaded.SeenHandoffIDs) != MaxSeenHandoffs {
		t.Fatalf("seen = %d", len(loaded.SeenHandoffIDs))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %o", parent.Mode().Perm())
	}
}

func TestManualClaimSnapshotsScheduleBeforeConcurrentMutation(t *testing.T) {
	now := time.Now().UTC()
	st := State{}
	original := testSchedule(t, "manual")
	original.Prompt = "original prompt"
	if err := Upsert(&st, original, now); err != nil {
		t.Fatal(err)
	}
	snapshot, job, err := ClaimManual(&st, "manual", now)
	if err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Prompt = "new prompt"
	if err := Upsert(&st, updated, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if snapshot.Prompt != "original prompt" {
		t.Fatalf("claimed prompt changed: %q", snapshot.Prompt)
	}
	if err := CompleteJob(&st, job.ID, "launched", "run_1", ""); err != nil {
		t.Fatal(err)
	}
	if st.Jobs[0].Outcome != "launched" || st.Jobs[0].RuntimeRunID != "run_1" {
		t.Fatalf("job = %#v", st.Jobs[0])
	}
}

func TestStoreConcurrentUpdatesDoNotLoseWrites(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "state.json")}
	const workers = 40
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- store.Update(func(st *State) error {
				st.Jobs = append(st.Jobs, Job{ID: fmt.Sprintf("job-%d", i)})
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Jobs) != workers {
		t.Fatalf("lost updates: got %d jobs", len(st.Jobs))
	}
}
