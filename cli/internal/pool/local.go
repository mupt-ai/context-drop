package pool

import (
	"context"
	"errors"

	"contextdrop.dev/context-drop/internal/agents"
)

// Local is this machine's side of the pool: what the daemon reports and the
// calls it answers.
type Local interface {
	Agents(ctx context.Context) ([]Agent, error)
	Read(ctx context.Context, in LocalRead) (ReadResult, error)
	Send(ctx context.Context, in LocalSend) (SendResult, error)
}

// Machine is the Local for the sessions on this computer.
type Machine struct {
	Name string
	// NewEnv and Providers default to the agents package's.
	NewEnv    func() (*agents.Env, error)
	Providers func() []agents.Provider
}

func (m Machine) discover(ctx context.Context) (*agents.Env, []agents.Provider, agents.Index, error) {
	newEnv, providers := m.NewEnv, m.Providers
	if newEnv == nil {
		newEnv = agents.NewEnv
	}
	if providers == nil {
		providers = agents.Providers
	}
	env, err := newEnv()
	if err != nil {
		return nil, nil, agents.Index{}, err
	}
	ps := providers()
	return env, ps, agents.Discover(ctx, env, ps), nil
}

// Agents lists this machine's live sessions as org agents.
func (m Machine) Agents(ctx context.Context) ([]Agent, error) {
	env, _, idx, err := m.discover(ctx)
	if err != nil {
		return nil, err
	}
	agents.Locate(ctx, env, idx.Sessions)
	out := make([]Agent, 0, len(idx.Sessions))
	for _, s := range idx.Sessions {
		out = append(out, FromSession(m.Name, s))
	}
	return out, nil
}

// Read returns a local session's transcript.
func (m Machine) Read(ctx context.Context, in LocalRead) (ReadResult, error) {
	env, providers, idx, err := m.discover(ctx)
	if err != nil {
		return ReadResult{}, err
	}
	s, err := resolve(idx, in.ID)
	if err != nil {
		return ReadResult{}, err
	}
	located := []agents.Session{s}
	agents.Locate(ctx, env, located)
	p, err := agents.ProviderFor(providers, s.Agent)
	if err != nil {
		return ReadResult{}, err
	}
	msgs, err := p.Transcript(ctx, env, s)
	if err != nil {
		return ReadResult{}, err
	}
	return Narrow(FromSession(m.Name, located[0]), FromMessages(msgs), in.ReadOptions), nil
}

// Send delivers a message to a local session.
func (m Machine) Send(ctx context.Context, in LocalSend) (SendResult, error) {
	env, providers, idx, err := m.discover(ctx)
	if err != nil {
		return SendResult{}, err
	}
	s, err := resolve(idx, in.ID)
	if err != nil {
		return SendResult{}, err
	}
	got, err := agents.Send(ctx, env, providers, idx, s, in.Text, agents.SendOptions{From: in.From, ReplyTo: in.ReplyTo})
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{ID: m.Name + "/" + s.ID, Delivered: true, Via: got.Delivery}, nil
}

// resolve finds a session by its exact local ID; org IDs are never prefixes.
func resolve(idx agents.Index, id string) (agents.Session, error) {
	for _, s := range idx.Sessions {
		if s.ID == id {
			return s, nil
		}
	}
	return agents.Session{}, Errorf("not_found", "no live agent %s on this machine", id)
}

// AsError turns any error into a wire Error.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: "failed", Message: err.Error()}
}
