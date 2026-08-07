package pairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"contextdrop.dev/context-drop/internal/handoff"
)

const (
	DefaultInviteTTL = 10 * time.Minute
	MaxInviteTTL     = 15 * time.Minute
	maxMachineName   = 80
	maxMessageBody   = 4096
)

var (
	ErrUnauthorized     = errors.New("unauthorized")
	ErrInvalidInvite    = errors.New("invalid or expired join token")
	ErrMachineNotFound  = errors.New("machine not found")
	ErrAmbiguousMachine = errors.New("machine name is ambiguous")
	ErrMessageEmpty     = errors.New("message is empty")
	ErrConflict         = errors.New("pairing state conflict")
)

type Principal struct {
	ChainID   string `json:"chain_id"`
	MachineID string `json:"machine_id"`
}

type Chain struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type Machine struct {
	ID         string    `json:"id"`
	ChainID    string    `json:"chain_id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type Invite struct {
	TokenHash          string     `json:"token_hash"`
	ChainID            string     `json:"chain_id"`
	CreatedByMachineID string     `json:"created_by_machine_id"`
	CreatedAt          time.Time  `json:"created_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	UsedAt             *time.Time `json:"used_at,omitempty"`
}

type Session struct {
	TokenHash  string    `json:"token_hash"`
	ChainID    string    `json:"chain_id"`
	MachineID  string    `json:"machine_id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type Message struct {
	ID            string    `json:"id"`
	ChainID       string    `json:"chain_id"`
	FromMachineID string    `json:"from_machine_id"`
	ToMachineID   string    `json:"to_machine_id"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

type Store interface {
	CreateChain(ctx context.Context, machineName string, now time.Time) (Principal, string, Machine, error)
	CreateInvite(ctx context.Context, principal Principal, ttl time.Duration, now time.Time) (string, Invite, error)
	ConsumeInvite(ctx context.Context, token, machineName string, now time.Time) (Principal, string, Machine, error)
	AuthenticateSession(ctx context.Context, token string, now time.Time) (Principal, bool, error)
	ListMachines(ctx context.Context, principal Principal) ([]Machine, error)
	SendMessage(ctx context.Context, principal Principal, to, body string, now time.Time) (Message, error)
	ListMessages(ctx context.Context, principal Principal) ([]Message, error)
}

type HandoffStore interface {
	CreateHandoff(ctx context.Context, principal Principal, input handoff.Create, now time.Time) (handoff.Handoff, error)
	ListHandoffs(ctx context.Context, principal Principal, now time.Time) ([]handoff.Handoff, error)
	GetHandoff(ctx context.Context, principal Principal, id string, now time.Time) (handoff.Handoff, error)
	SetHandoffState(ctx context.Context, principal Principal, id string, next handoff.State, now time.Time) (handoff.Handoff, error)
}

type state struct {
	Version  int                        `json:"version"`
	Chains   map[string]Chain           `json:"chains"`
	Machines map[string]Machine         `json:"machines"`
	Sessions map[string]Session         `json:"sessions"`
	Invites  map[string]Invite          `json:"invites"`
	Messages map[string]Message         `json:"messages"`
	Handoffs map[string]handoff.Handoff `json:"handoffs"`
}

func newState() state {
	st := state{Version: 1}
	initState(&st)
	return st
}

func initState(st *state) {
	if st.Version == 0 {
		st.Version = 1
	}
	if st.Chains == nil {
		st.Chains = make(map[string]Chain)
	}
	if st.Machines == nil {
		st.Machines = make(map[string]Machine)
	}
	if st.Sessions == nil {
		st.Sessions = make(map[string]Session)
	}
	if st.Invites == nil {
		st.Invites = make(map[string]Invite)
	}
	if st.Messages == nil {
		st.Messages = make(map[string]Message)
	}
	if st.Handoffs == nil {
		st.Handoffs = make(map[string]handoff.Handoff)
	}
}

type backend interface {
	Load(ctx context.Context) (state, int64, bool, error)
	Save(ctx context.Context, st state, generation int64, exists bool) error
}

type atomicBackend interface {
	Update(ctx context.Context, fn func(*state) error) error
}

type JSONStore struct {
	mu      sync.Mutex
	backend backend
}

func NewMemory() *JSONStore {
	return &JSONStore{backend: newMemoryBackend()}
}

func (s *JSONStore) CreateChain(ctx context.Context, machineName string, now time.Time) (Principal, string, Machine, error) {
	machineName = CleanMachineName(machineName)

	var principal Principal
	var sessionToken string
	var machine Machine
	err := s.update(ctx, func(st *state) error {
		pruneExpiredInvites(st, now)

		chainID, err := newID("chain")
		if err != nil {
			return err
		}
		st.Chains[chainID] = Chain{ID: chainID, CreatedAt: now}

		machineID, err := newID("mach")
		if err != nil {
			return err
		}
		machine = Machine{
			ID:         machineID,
			ChainID:    chainID,
			Name:       machineName,
			CreatedAt:  now,
			LastSeenAt: now,
		}
		st.Machines[machine.ID] = machine

		token, session, err := newSession(chainID, machine.ID, now)
		if err != nil {
			return err
		}
		st.Sessions[session.TokenHash] = session

		principal = Principal{ChainID: chainID, MachineID: machine.ID}
		sessionToken = token
		return nil
	})
	return principal, sessionToken, machine, err
}

func (s *JSONStore) CreateInvite(ctx context.Context, principal Principal, ttl time.Duration, now time.Time) (string, Invite, error) {
	ttl, err := normalizeInviteTTL(ttl)
	if err != nil {
		return "", Invite{}, err
	}

	var token string
	var invite Invite
	err = s.update(ctx, func(st *state) error {
		pruneExpiredInvites(st, now)
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		token, err = newToken("cdj")
		if err != nil {
			return err
		}
		invite = Invite{
			TokenHash:          tokenHash(token),
			ChainID:            principal.ChainID,
			CreatedByMachineID: principal.MachineID,
			CreatedAt:          now,
			ExpiresAt:          now.Add(ttl),
		}
		st.Invites[invite.TokenHash] = invite
		return nil
	})
	return token, invite, err
}

func (s *JSONStore) ConsumeInvite(ctx context.Context, token, machineName string, now time.Time) (Principal, string, Machine, error) {
	token = strings.TrimSpace(token)
	machineName = CleanMachineName(machineName)
	if token == "" {
		return Principal{}, "", Machine{}, ErrInvalidInvite
	}

	var principal Principal
	var sessionToken string
	var machine Machine
	err := s.update(ctx, func(st *state) error {
		pruneExpiredInvites(st, now)

		hash := tokenHash(token)
		invite, ok := st.Invites[hash]
		if !ok || invite.UsedAt != nil || !now.Before(invite.ExpiresAt) {
			return ErrInvalidInvite
		}
		if _, ok := st.Chains[invite.ChainID]; !ok {
			return ErrInvalidInvite
		}

		id, err := newID("mach")
		if err != nil {
			return err
		}
		machine = Machine{
			ID:         id,
			ChainID:    invite.ChainID,
			Name:       machineName,
			CreatedAt:  now,
			LastSeenAt: now,
		}
		st.Machines[machine.ID] = machine

		usedAt := now
		invite.UsedAt = &usedAt
		st.Invites[hash] = invite

		var session Session
		var sessionErr error
		sessionToken, session, sessionErr = newSession(invite.ChainID, machine.ID, now)
		if sessionErr != nil {
			return sessionErr
		}
		st.Sessions[session.TokenHash] = session

		principal = Principal{ChainID: invite.ChainID, MachineID: machine.ID}
		return nil
	})
	return principal, sessionToken, machine, err
}

func (s *JSONStore) AuthenticateSession(ctx context.Context, token string, now time.Time) (Principal, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Principal{}, false, nil
	}

	var principal Principal
	var ok bool
	err := s.update(ctx, func(st *state) error {
		session, found := st.Sessions[tokenHash(token)]
		if !found {
			return nil
		}
		machine, found := st.Machines[session.MachineID]
		if !found || machine.ChainID != session.ChainID {
			return nil
		}
		if _, found := st.Chains[session.ChainID]; !found {
			return nil
		}
		session.LastSeenAt = now
		st.Sessions[session.TokenHash] = session
		machine.LastSeenAt = now
		st.Machines[machine.ID] = machine
		principal = Principal{ChainID: session.ChainID, MachineID: session.MachineID}
		ok = true
		return nil
	})
	return principal, ok, err
}

func (s *JSONStore) ListMachines(ctx context.Context, principal Principal) ([]Machine, error) {
	var out []Machine
	err := s.view(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		for _, machine := range st.Machines {
			if machine.ChainID == principal.ChainID {
				out = append(out, machine)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].CreatedAt.Equal(out[j].CreatedAt) {
				return out[i].ID < out[j].ID
			}
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		})
		return nil
	})
	return out, err
}

func (s *JSONStore) SendMessage(ctx context.Context, principal Principal, to, body string, now time.Time) (Message, error) {
	to = strings.TrimSpace(to)
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, ErrMessageEmpty
	}
	if utf8.RuneCountInString(body) > maxMessageBody {
		return Message{}, fmt.Errorf("message exceeds %d characters", maxMessageBody)
	}

	var msg Message
	err := s.update(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		toMachine, err := resolveMachine(st, principal.ChainID, to)
		if err != nil {
			return err
		}
		id, err := newID("msg")
		if err != nil {
			return err
		}
		msg = Message{
			ID:            id,
			ChainID:       principal.ChainID,
			FromMachineID: principal.MachineID,
			ToMachineID:   toMachine.ID,
			Body:          body,
			CreatedAt:     now,
		}
		st.Messages[msg.ID] = msg
		return nil
	})
	return msg, err
}

func (s *JSONStore) ListMessages(ctx context.Context, principal Principal) ([]Message, error) {
	var out []Message
	err := s.view(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		for _, msg := range st.Messages {
			if msg.ChainID == principal.ChainID && msg.ToMachineID == principal.MachineID {
				out = append(out, msg)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].CreatedAt.Equal(out[j].CreatedAt) {
				return out[i].ID < out[j].ID
			}
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		})
		return nil
	})
	return out, err
}

func (s *JSONStore) CreateHandoff(ctx context.Context, principal Principal, input handoff.Create, now time.Time) (handoff.Handoff, error) {
	if err := handoff.ValidateCreate(input); err != nil {
		return handoff.Handoff{}, err
	}
	var out handoff.Handoff
	err := s.update(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		target, err := resolveMachine(st, principal.ChainID, input.TargetMachineID)
		if err != nil {
			return err
		}
		id, err := handoff.NewID()
		if err != nil {
			return err
		}
		out = handoff.Handoff{
			ID: id, ChainID: principal.ChainID, SourceMachineID: principal.MachineID,
			TargetMachineID: target.ID, Summary: strings.TrimSpace(input.Summary),
			RequestedAction: strings.TrimSpace(input.RequestedAction), Repository: strings.TrimSpace(input.Repository),
			BaseCommit: strings.TrimSpace(input.BaseCommit), SourceRunID: strings.TrimSpace(input.SourceRunID),
			Artifacts: append([]handoff.Artifact(nil), input.Artifacts...), CreatedAt: now,
			ExpiresAt: now.Add(input.TTL), RecipientState: handoff.StateAvailable,
		}
		st.Handoffs[id] = out
		return nil
	})
	return out, err
}

func (s *JSONStore) ListHandoffs(ctx context.Context, principal Principal, now time.Time) ([]handoff.Handoff, error) {
	var out []handoff.Handoff
	err := s.view(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		for _, h := range st.Handoffs {
			if h.ChainID == principal.ChainID && h.TargetMachineID == principal.MachineID {
				h.RecipientState = handoff.EffectiveState(h, now)
				out = append(out, h)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
		return nil
	})
	return out, err
}

func (s *JSONStore) GetHandoff(ctx context.Context, principal Principal, id string, now time.Time) (handoff.Handoff, error) {
	var out handoff.Handoff
	err := s.view(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		h, ok := st.Handoffs[id]
		if !ok || h.ChainID != principal.ChainID || h.TargetMachineID != principal.MachineID {
			return ErrMachineNotFound
		}
		h.RecipientState = handoff.EffectiveState(h, now)
		out = h
		return nil
	})
	return out, err
}

func (s *JSONStore) SetHandoffState(ctx context.Context, principal Principal, id string, next handoff.State, now time.Time) (handoff.Handoff, error) {
	var out handoff.Handoff
	err := s.update(ctx, func(st *state) error {
		if !validPrincipal(st, principal) {
			return ErrUnauthorized
		}
		h, ok := st.Handoffs[id]
		if !ok || h.ChainID != principal.ChainID || h.TargetMachineID != principal.MachineID {
			return ErrMachineNotFound
		}
		if !now.Before(h.ExpiresAt) {
			return fmt.Errorf("handoff expired")
		}
		current := h.RecipientState
		recoveringClaim := current == handoff.StateAccepting && h.RecipientStateAt != nil && now.Sub(*h.RecipientStateAt) > 5*time.Minute
		allowed := (next == handoff.StateInspected && current == handoff.StateAvailable) ||
			(next == handoff.StateAccepting && (current == handoff.StateAvailable || current == handoff.StateInspected || recoveringClaim)) ||
			(next == handoff.StateAccepted && current == handoff.StateAccepting) ||
			(next == handoff.StateRejected && (current == handoff.StateAvailable || current == handoff.StateInspected || current == handoff.StateAccepting))
		if !allowed {
			return fmt.Errorf("invalid handoff state transition from %s to %s", current, next)
		}
		h.RecipientState = next
		at := now
		h.RecipientStateAt = &at
		st.Handoffs[id] = h
		out = h
		return nil
	})
	return out, err
}

func (s *JSONStore) update(ctx context.Context, fn func(*state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.backend.(atomicBackend); ok {
		return b.Update(ctx, fn)
	}

	const attempts = 5
	for i := 0; i < attempts; i++ {
		st, generation, exists, err := s.backend.Load(ctx)
		if err != nil {
			return err
		}
		initState(&st)
		if err := fn(&st); err != nil {
			return err
		}
		err = s.backend.Save(ctx, st, generation, exists)
		if errors.Is(err, ErrConflict) {
			continue
		}
		return err
	}
	return ErrConflict
}

func (s *JSONStore) view(ctx context.Context, fn func(*state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, _, _, err := s.backend.Load(ctx)
	if err != nil {
		return err
	}
	initState(&st)
	return fn(&st)
}

func normalizeInviteTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		return DefaultInviteTTL, nil
	}
	if ttl < 0 {
		return 0, fmt.Errorf("join token ttl must be positive")
	}
	if ttl > MaxInviteTTL {
		return 0, fmt.Errorf("join token ttl exceeds max of %s", MaxInviteTTL)
	}
	return ttl, nil
}

func validPrincipal(st *state, principal Principal) bool {
	if principal.ChainID == "" || principal.MachineID == "" {
		return false
	}
	machine, ok := st.Machines[principal.MachineID]
	return ok && machine.ChainID == principal.ChainID
}

func resolveMachine(st *state, chainID, to string) (Machine, error) {
	if to == "" {
		return Machine{}, ErrMachineNotFound
	}
	if machine, ok := st.Machines[to]; ok && machine.ChainID == chainID {
		return machine, nil
	}
	var matches []Machine
	for _, machine := range st.Machines {
		if machine.ChainID == chainID && machine.Name == to {
			matches = append(matches, machine)
		}
	}
	switch len(matches) {
	case 0:
		return Machine{}, ErrMachineNotFound
	case 1:
		return matches[0], nil
	default:
		return Machine{}, ErrAmbiguousMachine
	}
}

func pruneExpiredInvites(st *state, now time.Time) {
	for hash, invite := range st.Invites {
		if invite.UsedAt == nil && now.After(invite.ExpiresAt) {
			delete(st.Invites, hash)
		}
	}
}

func newSession(chainID, machineID string, now time.Time) (string, Session, error) {
	token, err := newToken("cds")
	if err != nil {
		return "", Session{}, err
	}
	session := Session{
		TokenHash:  tokenHash(token),
		ChainID:    chainID,
		MachineID:  machineID,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	return token, session, nil
}

func newID(prefix string) (string, error) {
	token, err := newToken(prefix)
	if err != nil {
		return "", err
	}
	return token, nil
}

func newToken(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func CleanMachineName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "machine"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || r == 127:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
		if b.Len() >= maxMachineName {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "machine"
	}
	return out
}
