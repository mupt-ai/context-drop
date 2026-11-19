package pool

import (
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
)

// FromSession describes a local session as an org agent on machine.
func FromSession(machine string, s agents.Session) Agent {
	a := Agent{
		ID:         machine + "/" + s.ID,
		Machine:    machine,
		Kind:       s.Agent,
		Role:       RoleMain,
		Status:     s.Status,
		Cwd:        s.Cwd,
		Name:       firstNonEmpty(s.Name, s.Title),
		StartedAt:  stamp(s.StartedAt),
		LastActive: stamp(s.UpdatedAt),
	}
	if s.Subagent {
		a.Role = RoleSubagent
		a.Root = machine + "/" + s.ParentID
		a.Parent = a.Root
		a.AgentType = s.Title
		a.Name = s.Name
	}
	switch {
	case s.Location != nil && s.Location.Mux != "":
		l := s.Location
		a.Location = &Location{Mux: l.Mux, Session: named(l.Session, ""), Window: named(l.Window, ""), Pane: named(l.Pane, l.Label)}
		a.LocationLabel = l.String()
	case s.App != "":
		a.Location = &Location{App: strings.TrimSuffix(s.App, "-desktop")}
		a.LocationLabel = agents.AppLabel(s.App)
	case s.TTY != "":
		a.Location = &Location{Terminal: true}
	}
	return a
}

func named(id, label string) *Named {
	if id == "" {
		return nil
	}
	return &Named{ID: id, Label: label}
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// FromMessages turns a transcript into entries.
func FromMessages(msgs []agents.Message) []Entry {
	entries := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		e := Entry{Text: m.Text, Time: stamp(m.Time)}
		switch m.Role {
		case "user", "assistant", "recap", "error":
			e.Type = m.Role
		case "system":
			if !strings.HasPrefix(m.Text, "Conversation compacted") {
				continue
			}
			e = Entry{Type: "compacted", Trigger: compactTrigger(m.Text), Time: e.Time}
		default:
			e.Type = "assistant"
		}
		entries = append(entries, e)
	}
	return entries
}

func compactTrigger(text string) string {
	if strings.Contains(text, "(manual)") {
		return "manual"
	}
	return "auto"
}

// Narrow applies read options to a whole transcript: the current context run
// unless All, then only entries after the cursor, then the last N.
func Narrow(agent Agent, entries []Entry, opts ReadOptions) ReadResult {
	starts := []int{0}
	for i, e := range entries {
		if e.Type == "compacted" && i > 0 {
			starts = append(starts, i)
		}
	}
	res := ReadResult{Agent: agent, Runs: len(starts), Run: len(starts)}
	if !opts.All {
		entries = entries[starts[len(starts)-1]:]
	} else {
		res.Run = 1
	}
	if len(entries) > 0 {
		res.Cursor = entries[len(entries)-1].Time
	}
	if opts.After != "" {
		after, err := time.Parse(time.RFC3339Nano, opts.After)
		var kept []Entry
		for _, e := range entries {
			t, terr := time.Parse(time.RFC3339Nano, e.Time)
			if err != nil || terr != nil || t.After(after) {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if opts.Last > 0 && len(entries) > opts.Last {
		entries = entries[len(entries)-opts.Last:]
	}
	if entries == nil {
		entries = []Entry{}
	}
	res.Entries = entries
	if res.Cursor == "" {
		res.Cursor = opts.After
	}
	return res
}
