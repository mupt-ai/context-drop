package agents

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrNoSelf means the calling process does not run under any live session.
var ErrNoSelf = errors.New("not running inside a live agent session")

// Self finds the live top-level session that process pid runs under: the
// nearest ancestor process that is a session's process. A shell command an
// agent runs is a descendant of the agent, so this identifies the agent that
// called Context Drop without any flags. When that process serves several
// sessions (a Codex app-server holding many threads), it is ambiguous and an
// error says so; SelfCandidates lists them.
func Self(ctx context.Context, env *Env, idx Index, pid int) (Session, error) {
	found, err := SelfCandidates(ctx, env, idx, pid)
	switch {
	case err != nil:
		return Session{}, err
	case len(found) == 0:
		return Session{}, ErrNoSelf
	case len(found) > 1:
		ids := make([]string, len(found))
		for i, s := range found {
			ids[i] = s.ID
		}
		return Session{}, fmt.Errorf("this command runs under a process that serves several sessions (%s); name yours with --as", strings.Join(ids, ", "))
	}
	return found[0], nil
}

// SelfCandidates returns the sessions of the nearest ancestor of pid that is
// a session's process: one session, several when that process serves many,
// or none outside any session.
func SelfCandidates(ctx context.Context, env *Env, idx Index, pid int) ([]Session, error) {
	byPID := map[int][]Session{}
	for _, s := range idx.Sessions {
		if !s.Subagent && s.PID > 0 {
			byPID[s.PID] = append(byPID[s.PID], s)
		}
	}
	procs, err := env.Procs(ctx)
	if err != nil {
		return nil, err
	}
	for seen := 0; pid > 1 && seen < 64; seen++ {
		if found := byPID[pid]; len(found) > 0 {
			return found, nil
		}
		p, ok := procs[pid]
		if !ok {
			break
		}
		if strings.HasPrefix(commandBase(p.Command), "opencode") {
			// OpenCode sessions have no process of their own: the caller is
			// one of them, but it cannot be told which.
			var found []Session
			for _, s := range idx.Sessions {
				if s.Agent == OpenCode && !s.Subagent {
					found = append(found, s)
				}
			}
			return found, nil
		}
		pid = p.PPID
	}
	return nil, nil
}

// Change events reported by Changes.
const (
	ChangeStarted = "started"
	ChangeEnded   = "ended"
	ChangeStatus  = "status"
)

// Change is one difference between two discovery passes.
type Change struct {
	Time    time.Time `json:"time"`
	Event   string    `json:"event"`
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Session Session   `json:"session"`
}

// Changes lists sessions that started, ended, or changed status between two
// discovery passes, ordered by session ID within each kind of event.
func Changes(prev, next []Session, now time.Time) []Change {
	before := map[string]Session{}
	for _, s := range prev {
		before[s.ID] = s
	}
	var out []Change
	seen := map[string]bool{}
	for _, s := range next {
		seen[s.ID] = true
		old, ok := before[s.ID]
		switch {
		case !ok:
			out = append(out, Change{Time: now, Event: ChangeStarted, ID: s.ID, Agent: s.Agent, To: s.Status, Session: s})
		case old.Status != s.Status:
			out = append(out, Change{Time: now, Event: ChangeStatus, ID: s.ID, Agent: s.Agent, From: old.Status, To: s.Status, Session: s})
		}
	}
	for _, s := range prev {
		if !seen[s.ID] {
			out = append(out, Change{Time: now, Event: ChangeEnded, ID: s.ID, Agent: s.Agent, From: s.Status, Session: s})
		}
	}
	rank := map[string]int{ChangeEnded: 0, ChangeStarted: 1, ChangeStatus: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Event != out[j].Event {
			return rank[out[i].Event] < rank[out[j].Event]
		}
		return out[i].ID < out[j].ID
	})
	return out
}
