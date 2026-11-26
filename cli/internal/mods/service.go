package mods

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Capabilities a mod may list in `can`.
var capabilities = []string{"list", "read", "send", "call", "kv", "slack", "linear"}

// Caller is who a mod call is for.
type Caller struct {
	Kind    string `json:"kind"` // "machine", "agent", or "mod"
	Machine string `json:"machine,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Mod     string `json:"mod,omitempty"`
}

// Grant is what a running mod's token allows.
type Grant struct {
	Org   string
	Mod   string
	Can   map[string]bool
	Depth int
}

type token struct {
	Grant
	expires time.Time
}

// Service deploys, lists, and runs an org's mods.
type Service struct {
	store *Store
	rt    *Runtime
	log   *slog.Logger
	// APIBase is the URL mods use to call back into this server, e.g. http://127.0.0.1:8080.
	APIBase string
	// Timeout bounds one call (default 15 minutes).
	Timeout time.Duration

	mu     sync.Mutex
	tokens map[string]token
}

// NewService runs mods from store with rt.
func NewService(store *Store, rt *Runtime, apiBase string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, rt: rt, log: log, APIBase: apiBase, Timeout: 15 * time.Minute, tokens: map[string]token{}}
}

// Error is a failure with a code the API returns as is.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

func errf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// check validates a mod's metadata.
func check(m Meta) error {
	if !ValidName(m.Name) {
		return errf("bad_request", "mod name %q: use lowercase letters, digits, and dashes, starting with a letter", m.Name)
	}
	for _, c := range m.Can {
		known := false
		for _, k := range capabilities {
			known = known || c == k
		}
		if !known {
			return errf("bad_request", "unknown capability %q (use %s)", c, strings.Join(capabilities, ", "))
		}
	}
	return nil
}

// Deploy checks source and makes it the org's mod of that name, replacing any earlier one.
func (s *Service) Deploy(ctx context.Context, org, author, source string) (Summary, error) {
	if len(source) > 1<<20 {
		return Summary{}, errf("bad_request", "a mod is one file of at most 1 MB")
	}
	meta, err := s.rt.Describe(ctx, source)
	if err != nil {
		return Summary{}, errf("bad_request", "%v", err)
	}
	if err := check(meta); err != nil {
		return Summary{}, err
	}
	m, err := s.store.Put(org, meta, source, author)
	if err != nil {
		return Summary{}, err
	}
	s.log.Info("mod deployed", "org", org, "mod", meta.Name, "author", author)
	return summaryOf(&m), nil
}

// Summary is one row of `context-drop mods`.
type Summary struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Can         []string        `json:"can"`
	Input       json.RawMessage `json:"input"`
	Author      string          `json:"author"`
	Deployed    time.Time       `json:"deployed"`
}

func summaryOf(m *Mod) Summary {
	return Summary{Name: m.Name, Description: m.Description, Can: m.Can, Input: m.Input, Author: m.Author, Deployed: m.Deployed}
}

// List returns the org's mods.
func (s *Service) List(org string) ([]Summary, error) {
	mods, err := s.store.List(org)
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, m := range mods {
		out = append(out, summaryOf(m))
	}
	return out, nil
}

// Grant returns what a mod token allows, if it is live.
func (s *Service) Grant(tok string) (Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tok]
	if !ok || time.Now().After(t.expires) {
		delete(s.tokens, tok)
		return Grant{}, false
	}
	return t.Grant, true
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "cdx_" + hex.EncodeToString(b)
}

// Call runs a mod. depth counts nested mod-to-mod calls.
func (s *Service) Call(ctx context.Context, org, name string, input json.RawMessage, caller Caller, depth int) (json.RawMessage, error) {
	if depth > 4 {
		return nil, errf("bad_request", "mods called each other more than 4 deep")
	}
	m, src, err := s.store.Get(org, name)
	if errors.Is(err, ErrNotFound) {
		return nil, errf("not_found", "org %s has no mod %q", org, name)
	}
	if err != nil {
		return nil, err
	}
	can := map[string]bool{}
	for _, c := range m.Can {
		can[c] = true
	}
	tok := newToken()
	s.mu.Lock()
	s.tokens[tok] = token{Grant: Grant{Org: org, Mod: name, Can: can, Depth: depth}, expires: time.Now().Add(s.Timeout + time.Minute)}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.tokens, tok)
		s.mu.Unlock()
	}()
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	api, _ := url.Parse(s.APIBase)
	out, err := s.rt.exec(ctx, src, invocation{Mode: "run", Input: input, Caller: caller,
		API: &API{Base: s.APIBase, Org: org, Token: tok}}, api.Host, s.Timeout)
	var modErr *ErrMod
	if errors.As(err, &modErr) {
		return nil, errf("mod_failed", "%s: %s", name, modErr.Message)
	}
	return out, err
}

// KV runs a key-value operation for the mod a token belongs to.
func (s *Service) KV(g Grant, op, key string, value, expected json.RawMessage) (json.RawMessage, bool, error) {
	return s.store.KV(g.Org, g.Mod, op, key, value, expected)
}
