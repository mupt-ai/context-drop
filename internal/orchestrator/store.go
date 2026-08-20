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
	MaxPromptBytes  = 16000
)

const (
	ScheduleAgent   = "agent"
	ScheduleCommand = "command"
	ScheduleWatch   = "watch"
	OverlapSkip     = "skip"
)

type Schedule struct {
	Name                string        `json:"name"`
	Type                string        `json:"type"`
	Agent               string        `json:"agent,omitempty"`
	Backend             string        `json:"backend,omitempty"`
	Repo                string        `json:"repo,omitempty"`
	Prompt              string        `json:"prompt,omitempty"`
	Command             []string      `json:"command,omitempty"`
	Cwd                 string        `json:"cwd,omitempty"`
	WatchPane           string        `json:"watch_pane,omitempty"`
	WatchTarget         string        `json:"watch_target,omitempty"`
	LastWatchState      string        `json:"last_watch_state,omitempty"`
	Every               time.Duration `json:"every,omitempty"`
	Cron                string        `json:"cron,omitempty"`
	Timezone            string        `json:"timezone,omitempty"`
	Enabled             bool          `json:"enabled"`
	Overlap             string        `json:"overlap"`
	MissedRunPolicy     string        `json:"missed_run_policy"`
	Timeout             time.Duration `json:"timeout,omitempty"`
	MaxRetries          int           `json:"max_retries,omitempty"`
	ConsecutiveFailures int           `json:"consecutive_failures,omitempty"`
	AutoPauseAfter      int           `json:"auto_pause_after,omitempty"`
	NotifyOnInitiate    bool          `json:"notify_on_initiate"`
	CreatedAt           time.Time     `json:"created_at"`
	LastFiredAt         *time.Time    `json:"last_fired_at,omitempty"`
	NextRunAt           time.Time     `json:"next_run_at"`
}

type Job struct {
	ID            string     `json:"id"`
	OccurrenceKey string     `json:"occurrence_key"`
	ScheduleName  string     `json:"schedule_name"`
	ScheduleType  string     `json:"schedule_type"`
	Agent         string     `json:"agent,omitempty"`
	Repo          string     `json:"repo,omitempty"`
	Backend       string     `json:"backend,omitempty"`
	RuntimeRunID  string     `json:"runtime_run_id,omitempty"`
	Status        string     `json:"status"`
	Outcome       string     `json:"outcome,omitempty"` // legacy compatibility
	Attempt       int        `json:"attempt,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
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
	normalizeState(&st)
	return st, nil
}

// normalizeState is an in-memory, non-destructive migration for legacy state.
// It is persisted by the next ordinary Update/Save transaction.
func normalizeState(st *State) {
	for i := range st.Schedules {
		s := &st.Schedules[i]
		if s.Type == "" {
			s.Type = ScheduleAgent
		}
		if s.Overlap == "" {
			s.Overlap = OverlapSkip
		}
		if s.MissedRunPolicy == "" {
			s.MissedRunPolicy = "latest"
		}
	}
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.ScheduleType == "" {
			j.ScheduleType = ScheduleAgent
		}
		if j.Status == "" {
			j.Status = legacyJobStatus(j.Outcome)
		}
		if j.OccurrenceKey == "" {
			j.OccurrenceKey = j.ID
		}
	}
}

func legacyJobStatus(outcome string) string {
	switch outcome {
	case "launching":
		return "failed"
	case "launched":
		// Legacy jobs only recorded that a worker was dispatched; they were
		// never live lifecycle records. Treat them as terminal history so an
		// upgrade cannot make every old launch permanently block overlap=skip.
		return "completed"
	case "launch_error":
		return "failed"
	case "":
		return "queued"
	default:
		return outcome
	}
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
		active := make([]Job, 0)
		terminal := make([]Job, 0, len(st.Jobs))
		for _, job := range st.Jobs {
			if job.Status == "queued" || job.Status == "running" {
				active = append(active, job)
			} else {
				terminal = append(terminal, job)
			}
		}
		terminalLimit := MaxJobs - len(active)
		if terminalLimit < 0 {
			terminalLimit = 0
		}
		if len(terminal) > terminalLimit {
			terminal = terminal[len(terminal)-terminalLimit:]
		}
		st.Jobs = append(terminal, active...)
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
	if in.Type == "" {
		in.Type = ScheduleAgent
	}
	if in.Overlap == "" {
		in.Overlap = OverlapSkip
	}
	if in.MissedRunPolicy == "" {
		in.MissedRunPolicy = "latest"
	}
	if in.Type != ScheduleAgent && in.Type != ScheduleCommand && in.Type != ScheduleWatch {
		return fmt.Errorf("type must be agent, command, or watch")
	}
	if in.Overlap != OverlapSkip {
		return fmt.Errorf("overlap policy %q is not safely supported; use skip", in.Overlap)
	}
	if in.MissedRunPolicy != "latest" {
		return fmt.Errorf("missed-run policy must be latest")
	}
	if in.Timeout < 0 || in.MaxRetries < 0 || in.MaxRetries > 10 || in.AutoPauseAfter < 0 {
		return fmt.Errorf("timeout, retries, and auto-pause must be bounded non-negative values")
	}
	if in.Backend != "" && in.Backend != "tmux" && in.Backend != "herdr" {
		return fmt.Errorf("backend must be tmux or herdr")
	}
	switch in.Type {
	case ScheduleAgent:
		if strings.TrimSpace(in.Agent) == "" || strings.TrimSpace(in.Prompt) == "" {
			return fmt.Errorf("agent and prompt are required")
		}
		if len(in.Prompt) > MaxPromptBytes {
			return fmt.Errorf("prompt must be at most %d bytes", MaxPromptBytes)
		}
	case ScheduleCommand:
		if len(in.Command) == 0 {
			return fmt.Errorf("command requires a non-empty argv array")
		}
		for _, arg := range in.Command {
			if arg == "" {
				return fmt.Errorf("command argv entries must not be empty")
			}
		}
	case ScheduleWatch:
		if in.Backend == "" || (strings.TrimSpace(in.WatchPane) == "" && strings.TrimSpace(in.WatchTarget) == "") {
			return fmt.Errorf("watch requires a backend and explicit pane or stable target")
		}
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
	dir := in.Repo
	if in.Type == ScheduleCommand {
		dir = in.Cwd
	}
	if in.Type != ScheduleWatch {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("working directory must be an existing local directory")
		}
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("working directory must be an absolute path")
		}
	}
	return nil
}

func Upsert(st *State, schedule Schedule, now time.Time) error {
	if schedule.Type == "" {
		schedule.Type = ScheduleAgent
	}
	if schedule.Overlap == "" {
		schedule.Overlap = OverlapSkip
	}
	if schedule.MissedRunPolicy == "" {
		schedule.MissedRunPolicy = "latest"
	}
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

type Claim struct {
	Schedule Schedule
	Job      Job
}

func ClaimDue(st *State, now time.Time) []Claim {
	var due []Claim
	for i := range st.Schedules {
		s := &st.Schedules[i]
		if !s.Enabled || now.Before(s.NextRunAt) {
			continue
		}
		occurrence := s.NextRunAt.UTC().Format(time.RFC3339Nano)
		fired := now
		s.LastFiredAt = &fired
		next, err := nextScheduleOccurrence(*s, now) // latest: coalesce missed occurrences
		if err != nil {
			s.Enabled = false
			continue
		}
		s.NextRunAt = next
		if hasActiveJob(*st, s.Name) {
			st.Jobs = append(st.Jobs, NewJobWithOccurrence(*s, "skipped", occurrence, now))
			continue
		}
		job := NewJobWithOccurrence(*s, "queued", occurrence, now)
		st.Jobs = append(st.Jobs, job)
		due = append(due, Claim{Schedule: *s, Job: job})
	}
	return due
}

// Due is retained for callers that only need schedule snapshots. New execution
// paths should use ClaimDue so the queued reservation is durable.
func Due(st *State, now time.Time) []Schedule {
	claims := ClaimDue(st, now)
	out := make([]Schedule, 0, len(claims))
	for _, claim := range claims {
		out = append(out, claim.Schedule)
	}
	return out
}

func hasActiveJob(st State, name string) bool {
	for _, j := range st.Jobs {
		if j.ScheduleName == name && (j.Status == "queued" || j.Status == "running") {
			return true
		}
	}
	return false
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
		if hasActiveJob(*st, name) {
			return Schedule{}, Job{}, fmt.Errorf("schedule %q already has an active job (overlap=skip)", name)
		}
		job := NewJobWithOccurrence(schedule, "queued", "manual:"+now.UTC().Format(time.RFC3339Nano), now)
		st.Jobs = append(st.Jobs, job)
		return schedule, job, nil
	}
	return Schedule{}, Job{}, fmt.Errorf("schedule %q not found", name)
}

func SetJobStatus(st *State, id, status, runtimeID, errorText string, now time.Time) error {
	valid := map[string]bool{"queued": true, "running": true, "completed": true, "failed": true, "unknown": true, "timed_out": true, "skipped": true}
	if !valid[status] {
		return fmt.Errorf("invalid job status %q", status)
	}
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.ID != id {
			continue
		}
		j.Status, j.Outcome, j.RuntimeRunID, j.Error = status, status, runtimeID, errorText
		if (status == "running" || status == "completed" || status == "failed" || status == "timed_out") && j.StartedAt == nil {
			at := now
			j.StartedAt = &at
		}
		if status == "completed" || status == "failed" || status == "unknown" || status == "timed_out" || status == "skipped" {
			at := now
			j.FinishedAt = &at
		}
		return nil
	}
	return fmt.Errorf("job %q not found", id)
}

func CompleteJob(st *State, id, outcome, runtimeID, errorText string) error {
	status := legacyJobStatus(outcome)
	if outcome == "launched" {
		status = "running"
	}
	return SetJobStatus(st, id, status, runtimeID, errorText, time.Now().UTC())
}

func NewJobWithOccurrence(schedule Schedule, status, occurrence string, now time.Time) Job {
	var b [8]byte
	_, _ = rand.Read(b[:])
	job := Job{ID: "job_" + hex.EncodeToString(b[:]), OccurrenceKey: schedule.Name + ":" + occurrence, ScheduleName: schedule.Name, ScheduleType: schedule.Type, Agent: schedule.Agent, Repo: schedule.Repo, Backend: schedule.Backend, Status: status, Outcome: status, CreatedAt: now}
	if status == "running" {
		at := now
		job.StartedAt = &at
	}
	if status == "skipped" {
		at := now
		job.FinishedAt = &at
	}
	return job
}

func NewJob(schedule Schedule, outcome, runtimeID, errorText string, now time.Time) Job {
	job := NewJobWithOccurrence(schedule, legacyJobStatus(outcome), now.UTC().Format(time.RFC3339Nano), now)
	job.RuntimeRunID, job.Error, job.Outcome = runtimeID, errorText, outcome
	return job
}

func RecoverStaleLocalJobs(st *State, now time.Time) {
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.Status != "queued" && (j.ScheduleType == ScheduleAgent || j.Status != "running") {
			continue
		}
		_ = SetJobStatus(st, j.ID, "failed", j.RuntimeRunID, "daemon restarted before local job completion", now)
	}
}

func SetEnabled(st *State, name string, enabled bool, now time.Time) error {
	for i := range st.Schedules {
		if st.Schedules[i].Name == name {
			st.Schedules[i].Enabled = enabled
			if enabled {
				next, err := nextScheduleOccurrence(st.Schedules[i], now)
				if err != nil {
					return err
				}
				st.Schedules[i].NextRunAt = next
			}
			return nil
		}
	}
	return fmt.Errorf("schedule %q not found", name)
}
