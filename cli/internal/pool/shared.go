package pool

import (
	"context"
	"strings"
)

// Shared limits what the org can see and do on this machine. Only agents
// working in one of Dirs (or below it) are reported and answered for, unless
// All is set; ReadOnly refuses every send. An agent that is not shared looks
// the same to the org as one that does not exist.
type Shared struct {
	Local    Local
	All      bool
	Dirs     []string // absolute paths
	ReadOnly bool
}

func (s Shared) shares(a Agent) bool {
	if s.All {
		return true
	}
	for _, d := range s.Dirs {
		d = strings.TrimRight(d, "/")
		if a.Cwd == d || strings.HasPrefix(a.Cwd, d+"/") {
			return true
		}
	}
	return false
}

// Agents lists the shared agents.
func (s Shared) Agents(ctx context.Context) ([]Agent, error) {
	list, err := s.Local.Agents(ctx)
	if err != nil {
		return nil, err
	}
	kept := list[:0]
	for _, a := range list {
		if s.shares(a) {
			kept = append(kept, a)
		}
	}
	return kept, nil
}

// admit fails unless the session is a shared agent.
func (s Shared) admit(ctx context.Context, session string) error {
	if s.All {
		return nil
	}
	list, err := s.Agents(ctx)
	if err != nil {
		return err
	}
	for _, a := range list {
		if _, id, ok := SplitID(a.ID); ok && id == session {
			return nil
		}
	}
	return Errorf("not_found", "no live agent %s on this machine", session)
}

// Read reads a shared agent.
func (s Shared) Read(ctx context.Context, in LocalRead) (ReadResult, error) {
	if err := s.admit(ctx, in.ID); err != nil {
		return ReadResult{}, err
	}
	return s.Local.Read(ctx, in)
}

// Ask answers from a copy of a shared agent. It changes nothing, so read-only sharing allows it.
func (s Shared) Ask(ctx context.Context, in LocalAsk) (AskResult, error) {
	if err := s.admit(ctx, in.ID); err != nil {
		return AskResult{}, err
	}
	return s.Local.Ask(ctx, in)
}

// Send delivers to a shared agent, unless the machine is read-only.
func (s Shared) Send(ctx context.Context, in LocalSend) (SendResult, error) {
	if s.ReadOnly {
		return SendResult{}, Errorf("forbidden", "this machine shares its agents read-only; it does not accept messages from the org")
	}
	if err := s.admit(ctx, in.ID); err != nil {
		return SendResult{}, err
	}
	return s.Local.Send(ctx, in)
}
