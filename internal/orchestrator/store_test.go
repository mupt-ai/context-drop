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

func TestCronSchedulesAndCatchUp(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 7, 59, 0, 0, location).UTC()
	st := State{}
	daily := testSchedule(t, "digest")
	daily.Every = 0
	daily.Cron = "0 8,13,19 * * *"
	daily.Timezone = "America/Los_Angeles"
	if err := Upsert(&st, daily, now); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 6, 8, 0, 0, 0, location).UTC()
	if !st.Schedules[0].NextRunAt.Equal(want) {
		t.Fatalf("next = %s, want %s", st.Schedules[0].NextRunAt, want)
	}
	late := time.Date(2026, 8, 6, 20, 0, 0, 0, location).UTC()
	if got := len(Due(&st, late)); got != 1 {
		t.Fatalf("catch-up count = %d", got)
	}
	wantNext := time.Date(2026, 8, 7, 8, 0, 0, 0, location).UTC()
	if !st.Schedules[0].NextRunAt.Equal(wantNext) {
		t.Fatalf("next after catch-up = %s, want %s", st.Schedules[0].NextRunAt, wantNext)
	}
	if got := len(Due(&st, late)); got != 0 {
		t.Fatalf("duplicate catch-up count = %d", got)
	}
}

func TestCronScheduleRestartClaimsOnlyLatestCatchUp(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(t.TempDir(), "state.json")}
	st := State{}
	s := testSchedule(t, "restart")
	s.Every, s.Cron, s.Timezone = 0, "0 8,13,19 * * *", "America/Los_Angeles"
	created := time.Date(2026, 8, 1, 7, 0, 0, 0, location).UTC()
	if err := Upsert(&st, s, created); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 3, 20, 0, 0, 0, location).UTC()
	if got := len(Due(&restarted, now)); got != 1 {
		t.Fatalf("restart catch-up count = %d", got)
	}
	if got := len(Due(&restarted, now)); got != 0 {
		t.Fatalf("restart duplicate count = %d", got)
	}
	want := time.Date(2026, 8, 4, 8, 0, 0, 0, location).UTC()
	if !restarted.Schedules[0].NextRunAt.Equal(want) {
		t.Fatalf("next = %s, want %s", restarted.Schedules[0].NextRunAt, want)
	}
}

func TestCronFridayAndDST(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	next, err := nextCronOccurrence("0 21 * * 5", "America/Los_Angeles", time.Date(2026, 8, 6, 22, 0, 0, 0, location).UTC())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 7, 21, 0, 0, 0, location).UTC()
	if !next.Equal(want) {
		t.Fatalf("Friday next = %s, want %s", next, want)
	}
	beforeDST := time.Date(2026, 3, 7, 8, 0, 0, 0, location).UTC()
	next, err = nextCronOccurrence("0 8 * * *", "America/Los_Angeles", beforeDST)
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 3, 8, 8, 0, 0, 0, location).UTC()
	if !next.Equal(want) {
		t.Fatalf("DST next = %s (%s), want %s", next, next.In(location), want)
	}
}

func TestCronWildcardStepAndFallBackRunOnce(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	// */1 covers the whole day-of-month field and must retain standard cron
	// wildcard semantics, so this expression matches Mondays only.
	spec, err := parseCron("0 8 */1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	if spec.matches(time.Date(2026, 8, 4, 8, 0, 0, 0, location)) { // Tuesday
		t.Fatal("full-range stepped day-of-month made Monday cron match Tuesday")
	}

	firstFold := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC) // 01:30 PDT
	next, err := nextCronOccurrence("30 1 * * *", "America/Los_Angeles", firstFold)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 2, 1, 30, 0, 0, location).UTC()
	if !next.Equal(want) {
		t.Fatalf("fall-back duplicate was not skipped: next=%s (%s), want=%s", next, next.In(location), want)
	}
}

func TestCronParserRejectsFaultyExpressions(t *testing.T) {
	for _, expression := range []string{"0 8 * *", "60 8 * * *", "0 25 * * *", "0 8 * 13 *", "0 8 * * 8", "0 8 * * * *", "0 8/0 * * *", "* * * x *"} {
		if _, err := parseCron(expression); err == nil {
			t.Fatalf("expected error for %q", expression)
		}
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
	s.Cron = "0 8 * * *"
	s.Timezone = "America/Los_Angeles"
	if err := ValidateSchedule(s); err == nil {
		t.Fatal("expected mutually exclusive cadence error")
	}
	s = testSchedule(t, "ok")
	s.Repo = "."
	if err := ValidateSchedule(s); err == nil {
		t.Fatal("expected absolute repo error")
	}
}

func TestStorePermissionsAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := Store{Path: path}
	st := State{SeenMessageIDs: map[string]string{}}
	for i := 0; i < MaxJobs+20; i++ {
		st.Jobs = append(st.Jobs, Job{ID: fmt.Sprint(i)})
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
