package summarize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
)

// Input is one live session and its transcript.
type Input struct {
	Session   agents.Session
	Messages  []agents.Message
	Subagents []agents.Session
	// Err is set when the transcript could not be read.
	Err error
}

// SessionSummary is one session's entry in a report.
type SessionSummary struct {
	ID           string    `json:"id"`
	Agent        string    `json:"agent"`
	Name         string    `json:"name,omitempty"`
	Cwd          string    `json:"cwd,omitempty"`
	Status       string    `json:"status"`
	Subagents    int       `json:"subagents,omitempty"`
	Changed      bool      `json:"changed"`
	Summary      string    `json:"summary"`
	SummarizedAt time.Time `json:"summarized_at"`
	LastActivity time.Time `json:"last_activity,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// Report is the result of one summarize run.
type Report struct {
	Model       string           `json:"model"`
	GeneratedAt time.Time        `json:"generated_at"`
	Overview    string           `json:"overview,omitempty"`
	Sessions    []SessionSummary `json:"sessions"`
}

type stateEntry struct {
	Agent        string    `json:"agent"`
	NativeID     string    `json:"native_id"`
	Fingerprint  string    `json:"fingerprint"`
	MessageCount int       `json:"message_count"`
	LastMessage  time.Time `json:"last_message_at"`
	Summary      string    `json:"summary"`
	SummarizedAt time.Time `json:"summarized_at"`
}

type state struct {
	Sessions    map[string]stateEntry `json:"sessions"`
	Overview    string                `json:"overview,omitempty"`
	OverviewKey string                `json:"overview_key,omitempty"`
}

// Summarizer produces reports and remembers what it last summarized.
type Summarizer struct {
	Model     Model
	StatePath string
	// Force re-summarizes sessions even when their transcripts did not change.
	Force bool
	// Overview adds a cross-session "what's going on" paragraph.
	Overview    bool
	Concurrency int
	Now         func() time.Time
}

// StatePath returns the default location of the summary state file.
func StatePath(contextDropHome string) string {
	return filepath.Join(contextDropHome, "summaries.json")
}

const systemPrompt = `You summarize what a coding agent session is doing for the developer who owns it. ` +
	`Be concrete and brief: the task, what has been done, the current state, and anything that needs the developer ` +
	`(questions, blockers, failures, approvals). Do not invent details that are not in the transcript. ` +
	`Answer with 2-4 sentences of plain text, no headings.`

// Run summarizes inputs, calling the model only for sessions that changed
// since the last run.
func (s *Summarizer) Run(ctx context.Context, inputs []Input) (Report, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	st := s.load()
	report := Report{Model: s.Model.Name(), GeneratedAt: now()}
	report.Sessions = make([]SessionSummary, len(inputs))
	conc := s.Concurrency
	if conc <= 0 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, in := range inputs {
		wg.Add(1)
		go func(i int, in Input) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mu.Lock()
			prev, seen := st.Sessions[in.Session.ID]
			mu.Unlock()
			out := SessionSummary{
				ID: in.Session.ID, Agent: in.Session.Agent, Name: firstNonEmpty(in.Session.Name, in.Session.Title),
				Cwd: in.Session.Cwd, Status: in.Session.Status, Subagents: len(in.Subagents),
			}
			if len(in.Messages) > 0 {
				out.LastActivity = in.Messages[len(in.Messages)-1].Time
			}
			if in.Err != nil {
				out.Error = in.Err.Error()
				out.Summary, out.SummarizedAt = prev.Summary, prev.SummarizedAt
				report.Sessions[i] = out
				return
			}
			fp := fingerprint(in)
			if seen && prev.Fingerprint == fp && !s.Force {
				out.Summary, out.SummarizedAt = prev.Summary, prev.SummarizedAt
				report.Sessions[i] = out
				return
			}
			if len(in.Messages) == 0 {
				out.Changed = !seen
				out.Summary = "No conversation yet."
				out.SummarizedAt = now()
			} else {
				text, err := s.Model.Complete(ctx, systemPrompt, sessionPrompt(in, prev, seen && !s.Force))
				if err != nil {
					out.Error = err.Error()
					out.Summary, out.SummarizedAt = prev.Summary, prev.SummarizedAt
					report.Sessions[i] = out
					return
				}
				out.Changed = true
				out.Summary = text
				out.SummarizedAt = now()
			}
			mu.Lock()
			st.Sessions[in.Session.ID] = stateEntry{
				Agent: in.Session.Agent, NativeID: in.Session.NativeID, Fingerprint: fp,
				MessageCount: len(in.Messages), LastMessage: out.LastActivity,
				Summary: out.Summary, SummarizedAt: out.SummarizedAt,
			}
			mu.Unlock()
			report.Sessions[i] = out
		}(i, in)
	}
	wg.Wait()

	if s.Overview && len(report.Sessions) > 0 {
		key := overviewKey(report.Sessions)
		if key == st.OverviewKey && st.Overview != "" && !s.Force {
			report.Overview = st.Overview
		} else if text, err := s.Model.Complete(ctx, overviewSystem, overviewPrompt(report.Sessions)); err == nil {
			report.Overview = text
			st.Overview, st.OverviewKey = text, key
		} else {
			report.Overview = "(overview failed: " + err.Error() + ")"
		}
	}
	return report, s.save(st)
}

const overviewSystem = `You brief a developer on all of their running coding agents at once. ` +
	`Write 2-5 short bullet points ("- ") covering what is happening overall and which sessions need attention, ` +
	`citing session IDs. Plain text only.`

func overviewPrompt(sessions []SessionSummary) string {
	var b strings.Builder
	b.WriteString("Per-session summaries:\n\n")
	for _, s := range sessions {
		fmt.Fprintf(&b, "[%s] %s, status %s, in %s", s.ID, s.Agent, s.Status, s.Cwd)
		if s.Name != "" {
			fmt.Fprintf(&b, ", %q", s.Name)
		}
		if s.Subagents > 0 {
			fmt.Fprintf(&b, ", %d live subagents", s.Subagents)
		}
		fmt.Fprintf(&b, "\n%s\n\n", s.Summary)
	}
	return b.String()
}

func overviewKey(sessions []SessionSummary) string {
	h := sha256.New()
	for _, s := range sessions {
		fmt.Fprintf(h, "%s|%s|%s\n", s.ID, s.Status, s.Summary)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// maxPromptChars bounds the transcript excerpt sent to the model.
const maxPromptChars = 24000

func sessionPrompt(in Input, prev stateEntry, incremental bool) string {
	s := in.Session
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s (%s agent), status %s, working directory %s.\n", s.ID, s.Agent, s.Status, s.Cwd)
	if name := firstNonEmpty(s.Name, s.Title); name != "" {
		fmt.Fprintf(&b, "Session name: %s\n", name)
	}
	for _, sub := range in.Subagents {
		fmt.Fprintf(&b, "Live subagent %s: %s\n", sub.ID, firstNonEmpty(sub.Name, sub.Title, sub.NativeID))
	}
	msgs := in.Messages
	if incremental && prev.Summary != "" {
		fmt.Fprintf(&b, "\nPrevious summary (as of %s):\n%s\n", prev.SummarizedAt.Format(time.RFC3339), prev.Summary)
		msgs = newSince(msgs, prev)
		b.WriteString("\nNew transcript messages since then:\n")
	} else {
		b.WriteString("\nRecent transcript:\n")
	}
	b.WriteString(excerpt(msgs, maxPromptChars))
	if incremental && prev.Summary != "" {
		b.WriteString("\nUpdate the summary to reflect the current state.")
	} else {
		b.WriteString("\nSummarize this session.")
	}
	return b.String()
}

// newSince returns the messages added after the previous run.
func newSince(msgs []agents.Message, prev stateEntry) []agents.Message {
	if !prev.LastMessage.IsZero() {
		for i, m := range msgs {
			if m.Time.After(prev.LastMessage) {
				return msgs[i:]
			}
		}
	}
	if prev.MessageCount > 0 && prev.MessageCount < len(msgs) {
		return msgs[prev.MessageCount:]
	}
	if len(msgs) > 20 {
		return msgs[len(msgs)-20:]
	}
	return msgs
}

// excerpt renders the most recent messages that fit in max characters.
func excerpt(msgs []agents.Message, max int) string {
	var lines []string
	total := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		text := truncate(strings.TrimSpace(m.Text), 4000)
		line := fmt.Sprintf("%s: %s", strings.ToUpper(m.Role), text)
		if total+len(line) > max && len(lines) > 0 {
			lines = append(lines, "[earlier messages omitted]")
			break
		}
		lines = append(lines, line)
		total += len(line)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n\n") + "\n"
}

func fingerprint(in Input) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|", in.Session.Status, len(in.Messages))
	if n := len(in.Messages); n > 0 {
		last := in.Messages[n-1]
		fmt.Fprintf(h, "%s|%s|%s", last.Role, last.Time.UTC().Format(time.RFC3339Nano), last.Text)
	}
	subs := make([]string, len(in.Subagents))
	for i, s := range in.Subagents {
		subs[i] = s.ID
	}
	sort.Strings(subs)
	fmt.Fprintf(h, "|%s", strings.Join(subs, ","))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (s *Summarizer) load() state {
	st := state{Sessions: map[string]stateEntry{}}
	data, err := os.ReadFile(s.StatePath)
	if err != nil {
		return st
	}
	if json.Unmarshal(data, &st) != nil || st.Sessions == nil {
		st.Sessions = map[string]stateEntry{}
	}
	return st
}

func (s *Summarizer) save(st state) error {
	if s.StatePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.StatePath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.StatePath)
}
