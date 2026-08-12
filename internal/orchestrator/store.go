package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/localhome"
)

const (
	MaxJobs         = 1000
	MaxSeenMessages = 5000
	MaxMessageJobs  = 1000
	MaxPromptBytes  = 64 * 1024
)

type Schedule struct {
	Name             string        `json:"name"`
	Agent            string        `json:"agent"`
	Backend          string        `json:"backend,omitempty"`
	Repo             string        `json:"repo"`
	Prompt           string        `json:"prompt"`
	Every            time.Duration `json:"every,omitempty"`
	Cron             string        `json:"cron,omitempty"`
	Timezone         string        `json:"timezone,omitempty"`
	Enabled          bool          `json:"enabled"`
	NotifyOnInitiate bool          `json:"notify_on_initiate"`
	CreatedAt        time.Time     `json:"created_at"`
	LastFiredAt      *time.Time    `json:"last_fired_at,omitempty"`
	NextRunAt        time.Time     `json:"next_run_at"`
}

type Job struct {
	ID           string    `json:"id"`
	ScheduleName string    `json:"schedule_name"`
	Agent        string    `json:"agent"`
	Repo         string    `json:"repo"`
	RuntimeRunID string    `json:"runtime_run_id,omitempty"`
	Outcome      string    `json:"outcome"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type MessageJob struct {
	MessageID           string         `json:"message_id"`
	Status              string         `json:"status"`
	ClaimedAt           time.Time      `json:"claimed_at"`
	ProcessingStartedAt *time.Time     `json:"processing_started_at,omitempty"`
	UpdatedAt           time.Time      `json:"updated_at"`
	SentAt              *time.Time     `json:"sent_at,omitempty"`
	Error               string         `json:"error,omitempty"`
	Latency             MessageLatency `json:"latency"`
}

type MessageLatency struct {
	MessageCreatedAt   *time.Time          `json:"message_created_at,omitempty"`
	HistoryMS          int64               `json:"history_ms"`
	QueueMS            int64               `json:"queue_ms"`
	WorkerQueueMS      int64               `json:"worker_queue_ms"`
	PromptBuildMS      int64               `json:"prompt_build_ms"`
	ResponderStartupMS int64               `json:"responder_startup_ms"`
	ResponderMS        int64               `json:"responder_ms"`
	FirstOutputMS      int64               `json:"first_output_ms"`
	ToolExecutionMS    int64               `json:"tool_execution_ms"`
	CompactionMS       int64               `json:"compaction_ms"`
	SendMS             int64               `json:"send_ms"`
	ServiceMS          int64               `json:"service_ms"`
	EndToEndMS         int64               `json:"end_to_end_ms"`
	PromptBytes        int                 `json:"prompt_bytes"`
	ColdStart          bool                `json:"cold_start"`
	ModelRounds        []ModelRoundLatency `json:"model_rounds,omitempty"`
}

type ModelRoundLatency struct {
	DurationMS  int64  `json:"duration_ms"`
	Model       string `json:"model,omitempty"`
	ResponseID  string `json:"response_id,omitempty"`
	TotalTokens int64  `json:"total_tokens,omitempty"`
}

type State struct {
	Schedules           []Schedule            `json:"schedules"`
	Jobs                []Job                 `json:"jobs"`
	LastRuntimeError    string                `json:"last_runtime_error,omitempty"`
	IMessageInitialized bool                  `json:"imessage_initialized,omitempty"`
	IMessageChatID      string                `json:"imessage_chat_id,omitempty"`
	IMessageCursor      int64                 `json:"imessage_cursor,omitempty"`
	SeenMessageIDs      map[string]string     `json:"seen_message_ids,omitempty"`
	MessageJobs         map[string]MessageJob `json:"message_jobs,omitempty"`
	LastMessagePollAt   *time.Time            `json:"last_message_poll_at,omitempty"`
	LastMessageError    string                `json:"last_message_error,omitempty"`
}

type Store struct{ Path string }

func Paths() (dir, statePath, lockPath string, err error) {
	root, err := localhome.Root()
	if err != nil {
		return "", "", "", err
	}
	dir = filepath.Join(root, "daemon")
	return dir, filepath.Join(dir, "state.json"), filepath.Join(dir, "tick.lock"), nil
}

func NewStore() (Store, error) {
	_, path, _, err := Paths()
	return Store{Path: path}, err
}

func (s Store) Load() (State, error) {
	st := State{SeenMessageIDs: map[string]string{}, MessageJobs: map[string]MessageJob{}}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("read daemon state: %w", err)
	}
	if st.SeenMessageIDs == nil {
		st.SeenMessageIDs = map[string]string{}
	}
	if st.MessageJobs == nil {
		st.MessageJobs = map[string]MessageJob{}
	}
	return st, nil
}

func (s Store) Save(st State) error {
	return s.save(st)
}

// Update holds the process-wide state lock for one complete read-modify-write
// transaction. CLI commands and daemon ticks must use this instead of separate
// Load and Save calls so concurrent updates cannot overwrite each other.
func (s Store) Update(fn func(*State) error) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := LockFile(s.Path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	st, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&st); err != nil {
		return err
	}
	return s.save(st)
}

func (s Store) save(st State) error {
	pruneState(&st)
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.Path)
}

func pruneState(st *State) {
	if len(st.Jobs) > MaxJobs {
		st.Jobs = st.Jobs[len(st.Jobs)-MaxJobs:]
	}
	if st.SeenMessageIDs == nil {
		st.SeenMessageIDs = map[string]string{}
	}
	if len(st.SeenMessageIDs) > MaxSeenMessages {
		st.SeenMessageIDs = newestEntries(st.SeenMessageIDs, MaxSeenMessages)
	}
	if st.MessageJobs == nil {
		st.MessageJobs = map[string]MessageJob{}
	}
	if len(st.MessageJobs) > MaxMessageJobs {
		type item struct {
			id string
			at time.Time
		}
		items := make([]item, 0, len(st.MessageJobs))
		for id, job := range st.MessageJobs {
			items = append(items, item{id, job.UpdatedAt})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
		keep := make(map[string]MessageJob, MaxMessageJobs)
		for _, item := range items[:MaxMessageJobs] {
			keep[item.id] = st.MessageJobs[item.id]
		}
		st.MessageJobs = keep
	}
}

func newestEntries(entries map[string]string, limit int) map[string]string {
	type seen struct{ id, at string }
	items := make([]seen, 0, len(entries))
	for id, at := range entries {
		items = append(items, seen{id, at})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at > items[j].at })
	keep := make(map[string]string, limit)
	for _, item := range items[:limit] {
		keep[item.id] = item.at
	}
	return keep
}

func ValidateSchedule(in Schedule) error {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 80 {
		return fmt.Errorf("schedule name is required and must be at most 80 characters")
	}
	for _, r := range in.Name {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return fmt.Errorf("schedule name may contain only letters, numbers, dot, dash, and underscore")
		}
	}
	if strings.TrimSpace(in.Agent) == "" || strings.TrimSpace(in.Prompt) == "" {
		return fmt.Errorf("agent and prompt are required")
	}
	if in.Backend != "" && in.Backend != "tmux" && in.Backend != "herdr" {
		return fmt.Errorf("backend must be tmux or herdr")
	}
	if len(in.Prompt) > MaxPromptBytes {
		return fmt.Errorf("prompt must be at most %d bytes", MaxPromptBytes)
	}
	if (in.Every > 0) == (strings.TrimSpace(in.Cron) != "") {
		return fmt.Errorf("exactly one of --every or --cron is required")
	}
	if in.Every > 0 && in.Every < time.Minute {
		return fmt.Errorf("--every must be at least 1m")
	}
	if in.Cron != "" {
		if _, err := parseCron(in.Cron); err != nil {
			return err
		}
		if in.Timezone == "" {
			return fmt.Errorf("--timezone is required with --cron")
		}
		if _, err := time.LoadLocation(in.Timezone); err != nil {
			return fmt.Errorf("invalid IANA timezone %q", in.Timezone)
		}
	} else if in.Timezone != "" {
		return fmt.Errorf("--timezone requires --cron")
	}
	info, err := os.Stat(in.Repo)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repo must be an existing local directory")
	}
	if !filepath.IsAbs(in.Repo) {
		return fmt.Errorf("repo must be an absolute path")
	}
	return nil
}

func Upsert(st *State, schedule Schedule, now time.Time) error {
	if err := ValidateSchedule(schedule); err != nil {
		return err
	}
	next, err := nextScheduleOccurrence(schedule, now)
	if err != nil {
		return err
	}
	for i := range st.Schedules {
		if st.Schedules[i].Name == schedule.Name {
			schedule.CreatedAt = st.Schedules[i].CreatedAt
			schedule.LastFiredAt = st.Schedules[i].LastFiredAt
			schedule.NextRunAt = next
			st.Schedules[i] = schedule
			return nil
		}
	}
	schedule.CreatedAt = now
	schedule.NextRunAt = next
	st.Schedules = append(st.Schedules, schedule)
	sort.Slice(st.Schedules, func(i, j int) bool { return st.Schedules[i].Name < st.Schedules[j].Name })
	return nil
}

func Remove(st *State, name string) bool {
	for i := range st.Schedules {
		if st.Schedules[i].Name == name {
			st.Schedules = append(st.Schedules[:i], st.Schedules[i+1:]...)
			return true
		}
	}
	return false
}

func Due(st *State, now time.Time) []Schedule {
	var due []Schedule
	for i := range st.Schedules {
		s := &st.Schedules[i]
		if !s.Enabled || now.Before(s.NextRunAt) {
			continue
		}
		due = append(due, *s)
		fired := now
		s.LastFiredAt = &fired
		next, err := nextScheduleOccurrence(*s, now)
		if err != nil {
			s.Enabled = false
			continue
		}
		s.NextRunAt = next
	}
	return due
}

func nextScheduleOccurrence(schedule Schedule, after time.Time) (time.Time, error) {
	if schedule.Cron != "" {
		return nextCronOccurrence(schedule.Cron, schedule.Timezone, after)
	}
	return after.Add(schedule.Every), nil
}

func ClaimManual(st *State, name string, now time.Time) (Schedule, Job, error) {
	for _, schedule := range st.Schedules {
		if schedule.Name != name {
			continue
		}
		job := NewJob(schedule, "launching", "", "", now)
		st.Jobs = append(st.Jobs, job)
		return schedule, job, nil
	}
	return Schedule{}, Job{}, fmt.Errorf("schedule %q not found", name)
}

func CompleteJob(st *State, id, outcome, runtimeID, errorText string) error {
	for i := range st.Jobs {
		if st.Jobs[i].ID != id {
			continue
		}
		st.Jobs[i].Outcome = outcome
		st.Jobs[i].RuntimeRunID = runtimeID
		st.Jobs[i].Error = errorText
		return nil
	}
	return fmt.Errorf("job %q not found", id)
}

func NewJob(schedule Schedule, outcome, runtimeID, errorText string, now time.Time) Job {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return Job{ID: "job_" + hex.EncodeToString(b[:]), ScheduleName: schedule.Name, Agent: schedule.Agent, Repo: schedule.Repo, RuntimeRunID: runtimeID, Outcome: outcome, Error: errorText, CreatedAt: now}
}
