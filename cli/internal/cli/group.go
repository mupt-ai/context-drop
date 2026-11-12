package cli

import (
	"context"
	"errors"
	"os"
	"strings"

	"contextdrop.dev/context-drop/internal/agents"
	"github.com/spf13/cobra"
)

// selfPID is the process whose ancestry identifies the calling session; a seam for tests.
var selfPID = os.Getpid

// sessionFilter selects sessions by agent kind and by where they run. list,
// watch, send --all, and stop --all share it.
type sessionFilter struct {
	agent string
	in    string
	at    agents.Placement
}

func (f *sessionFilter) register(cmd *cobra.Command, verb string) {
	cmd.Flags().StringVar(&f.agent, "agent", "", verb+" one agent kind (claude, codex, pi, opencode)")
	cmd.Flags().StringVar(&f.in, "in", "", verb+" sessions running in this multiplexer (tmux, herdr, rex)")
	cmd.Flags().StringVar(&f.at.Session, "session", "", verb+" sessions in this multiplexer session (herdr: workspace)")
	cmd.Flags().StringVar(&f.at.Window, "window", "", verb+" sessions in this window (herdr: tab)")
	cmd.Flags().StringVar(&f.at.Pane, "pane", "", verb+" the session in this pane (rex: block)")
}

// set reports whether any filter is given.
func (f sessionFilter) set() bool { return f.agent != "" || f.byPlace() }

func (f sessionFilter) byPlace() bool { return f.in != "" || !f.at.IsZero() }

// apply locates sessions when a place is named, and keeps those that match.
func (f sessionFilter) apply(ctx context.Context, env *agents.Env, sessions []agents.Session) []agents.Session {
	if f.byPlace() {
		agents.Locate(ctx, env, sessions)
	}
	var out []agents.Session
	for _, s := range sessions {
		if (f.agent == "" || s.Agent == strings.ToLower(f.agent)) && inPlace(s, strings.ToLower(f.in), f.at) {
			out = append(out, s)
		}
	}
	return out
}

// inPlace reports whether s runs in the multiplexer and place given; with
// neither, every session matches.
func inPlace(s agents.Session, in string, at agents.Placement) bool {
	if in == "" && at.IsZero() {
		return true
	}
	return s.Location != nil && (in == "" || s.Location.Mux == in) && s.Location.Matches(at)
}

// topLevel drops subagents.
func topLevel(sessions []agents.Session) []agents.Session {
	var out []agents.Session
	for _, s := range sessions {
		if !s.Subagent {
			out = append(out, s)
		}
	}
	return out
}

// self finds the session the caller runs in: the one named by as, or the
// nearest agent session among the caller's ancestor processes.
func self(ctx context.Context, env *agents.Env, idx agents.Index, as string) (agents.Session, error) {
	if as != "" {
		return idx.Resolve(as)
	}
	return agents.Self(ctx, env, idx, selfPID())
}

// selves returns the IDs the caller may be: its own session, or every
// session of a shared process it runs under, since it cannot tell which.
func selves(ctx context.Context, env *agents.Env, idx agents.Index) map[string]bool {
	found, _ := agents.SelfCandidates(ctx, env, idx, selfPID())
	ids := map[string]bool{}
	for _, s := range found {
		ids[s.ID] = true
	}
	return ids
}

// withoutSelf drops the caller's own session (or possible sessions) from a group.
func withoutSelf(ctx context.Context, env *agents.Env, idx agents.Index, sessions []agents.Session) []agents.Session {
	me := selves(ctx, env, idx)
	var out []agents.Session
	for _, s := range sessions {
		if !me[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

var errNoGroup = errors.New("no live session matches")
