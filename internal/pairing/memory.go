package pairing

import (
	"context"
	"sync"

	"contextdrop.dev/context-drop/internal/handoff"
)

type memoryBackend struct {
	mu         sync.Mutex
	st         state
	generation int64
	exists     bool
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{st: newState()}
}

func (b *memoryBackend) Load(ctx context.Context) (state, int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return state{}, 0, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneState(b.st), b.generation, b.exists, nil
}

func (b *memoryBackend) Save(ctx context.Context, st state, generation int64, exists bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.generation != generation || b.exists != exists {
		return ErrConflict
	}
	b.st = cloneState(st)
	b.generation++
	b.exists = true
	return nil
}

func cloneState(in state) state {
	initState(&in)
	out := state{
		Version:  in.Version,
		Chains:   make(map[string]Chain, len(in.Chains)),
		Machines: make(map[string]Machine, len(in.Machines)),
		Sessions: make(map[string]Session, len(in.Sessions)),
		Invites:  make(map[string]Invite, len(in.Invites)),
		Messages: make(map[string]Message, len(in.Messages)),
		Handoffs: make(map[string]handoff.Handoff, len(in.Handoffs)),
	}
	for k, v := range in.Chains {
		out.Chains[k] = v
	}
	for k, v := range in.Machines {
		out.Machines[k] = v
	}
	for k, v := range in.Sessions {
		out.Sessions[k] = v
	}
	for k, v := range in.Invites {
		out.Invites[k] = v
	}
	for k, v := range in.Messages {
		out.Messages[k] = v
	}
	for k, v := range in.Handoffs {
		v.Artifacts = append([]handoff.Artifact(nil), v.Artifacts...)
		out.Handoffs[k] = v
	}
	return out
}
