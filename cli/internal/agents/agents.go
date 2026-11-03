// Package agents discovers live coding-agent sessions (Claude Code, Codex, Pi,
// OpenCode, and Muse) on this machine, gives each a deterministic ID, reads
// their transcripts, and delivers messages to them through each agent's own
// channel.
package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Agent kinds.
const (
	Claude   = "claude"
	Codex    = "codex"
	Pi       = "pi"
	OpenCode = "opencode"
	Muse     = "muse"
)

// Kinds lists every supported agent kind in display order.
var Kinds = []string{Claude, Codex, Pi, OpenCode, Muse}

var idPrefix = map[string]string{Claude: "cc", Codex: "cx", Pi: "pi", OpenCode: "oc", Muse: "mu"}

// Status values. Unknown means the agent's state could not be determined.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusUnknown = "unknown"
)

// Session is one live agent session or subagent.
type Session struct {
	// ID is deterministic: the same agent session always gets the same ID.
	ID       string `json:"id"`
	Agent    string `json:"agent"`
	NativeID string `json:"native_id"`
	// ParentID and ParentNativeID are set for subagents.
	ParentID       string    `json:"parent_id,omitempty"`
	ParentNativeID string    `json:"parent_native_id,omitempty"`
	Subagent       bool      `json:"subagent"`
	Name           string    `json:"name,omitempty"`
	Title          string    `json:"title,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	PID            int       `json:"pid,omitempty"`
	TTY            string    `json:"tty,omitempty"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	Transcript     string    `json:"transcript,omitempty"`
	// Delivery names the channel `send` will use.
	Delivery string `json:"delivery"`

	// internal routing details, not part of the public JSON.
	socket   string
	tmuxPane string
}

// Message is one transcript entry.
type Message struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	Time time.Time `json:"time,omitempty"`
}

// SendOptions control message delivery.
type SendOptions struct {
	// From names the sender in agents that display it (Claude Code peers).
	From string
}

// Provider adapts one agent kind.
type Provider interface {
	Kind() string
	Discover(ctx context.Context, env *Env) ([]Session, error)
	Transcript(ctx context.Context, env *Env, s Session) ([]Message, error)
	Send(ctx context.Context, env *Env, s Session, text string, opts SendOptions) error
}

// MakeID derives the stable short ID for an agent session.
func MakeID(agent, nativeID string) string {
	sum := sha256.Sum256([]byte(agent + ":" + nativeID))
	prefix := idPrefix[agent]
	if prefix == "" {
		prefix = "xx"
	}
	return prefix + "-" + hex.EncodeToString(sum[:])[:8]
}

func subagentNativeID(parent, child string) string { return parent + "/" + child }

// Index is the result of one discovery pass.
type Index struct {
	Sessions []Session `json:"sessions"`
	// Warnings lists providers that failed; discovery of the others continues.
	Warnings []string `json:"warnings,omitempty"`
}

// Providers returns every built-in provider.
func Providers() []Provider {
	return []Provider{claudeProvider{}, codexProvider{}, piProvider{}, openCodeProvider{}, museProvider{}}
}

// Discover runs every provider and returns live sessions in a deterministic order.
func Discover(ctx context.Context, env *Env, providers []Provider) Index {
	type result struct {
		sessions []Session
		err      error
	}
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			s, err := p.Discover(ctx, env)
			results[i] = result{s, err}
		}(i, p)
	}
	wg.Wait()
	var idx Index
	for i, r := range results {
		if r.err != nil {
			idx.Warnings = append(idx.Warnings, fmt.Sprintf("%s: %v", providers[i].Kind(), r.err))
		}
		idx.Sessions = append(idx.Sessions, r.sessions...)
	}
	for i := range idx.Sessions {
		s := &idx.Sessions[i]
		if s.ID == "" {
			s.ID = MakeID(s.Agent, s.NativeID)
		}
		if s.Status == "" {
			s.Status = StatusUnknown
		}
	}
	sortSessions(idx.Sessions)
	return idx
}

// sortSessions orders top-level sessions by agent then native ID, with each
// session's subagents directly after it.
func sortSessions(sessions []Session) {
	rank := map[string]int{}
	for i, k := range Kinds {
		rank[k] = i
	}
	parentKey := func(s Session) string {
		if s.Subagent {
			return s.ParentNativeID
		}
		return s.NativeID
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.Agent != b.Agent {
			return rank[a.Agent] < rank[b.Agent]
		}
		if pa, pb := parentKey(a), parentKey(b); pa != pb {
			return pa < pb
		}
		if a.Subagent != b.Subagent {
			return !a.Subagent
		}
		return a.NativeID < b.NativeID
	})
}

// ErrNotFound reports a target that matches no live session.
var ErrNotFound = errors.New("no live session matches")

// Resolve finds exactly one session by ID, native ID, native-ID prefix, or name.
func (idx Index) Resolve(target string) (Session, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return Session{}, fmt.Errorf("target is required")
	}
	matchers := []func(Session) bool{
		func(s Session) bool { return s.ID == target },
		func(s Session) bool { return s.NativeID == target },
		func(s Session) bool { return strings.HasPrefix(s.ID, target) },
		func(s Session) bool {
			return strings.HasPrefix(s.NativeID, target) || strings.HasPrefix(lastSegment(s.NativeID), target)
		},
		func(s Session) bool { return s.Name != "" && strings.EqualFold(s.Name, target) },
	}
	for _, match := range matchers {
		var found []Session
		for _, s := range idx.Sessions {
			if match(s) {
				found = append(found, s)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			ids := make([]string, len(found))
			for i, s := range found {
				ids[i] = s.ID
			}
			return Session{}, fmt.Errorf("%q is ambiguous: %s", target, strings.Join(ids, ", "))
		}
	}
	return Session{}, fmt.Errorf("%w %q; run `context-drop list`", ErrNotFound, target)
}

// Parent returns the parent of a subagent.
func (idx Index) Parent(s Session) (Session, bool) {
	for _, p := range idx.Sessions {
		if p.ID == s.ParentID {
			return p, true
		}
	}
	return Session{}, false
}

func lastSegment(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// ProviderFor returns the provider for an agent kind.
func ProviderFor(providers []Provider, kind string) (Provider, error) {
	for _, p := range providers {
		if p.Kind() == kind {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unsupported agent %q", kind)
}

// relayText wraps a message for a subagent that can only be reached through its parent.
func relayText(s Session, text, how string) string {
	label := s.Name
	if label == "" {
		label = lastSegment(s.NativeID)
	}
	return fmt.Sprintf("[context-drop relay] The following message is for your subagent %q (id %s). %s Do not act on it yourself.\n\n%s",
		label, lastSegment(s.NativeID), how, text)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len([]rune(s)) > max {
		r := []rune(s)
		return string(r[:max-1]) + "…"
	}
	return s
}

// Send delivers text to s and returns the session that received it. Claude
// Code and Muse subagents have no inbox of their own, so their message goes to
// the parent session with instructions to forward it.
func Send(ctx context.Context, env *Env, providers []Provider, idx Index, s Session, text string, opts SendOptions) (Session, error) {
	if strings.TrimSpace(text) == "" {
		return s, fmt.Errorf("message is empty")
	}
	p, err := ProviderFor(providers, s.Agent)
	if err != nil {
		return s, err
	}
	if s.Subagent && (s.Agent == Claude || s.Agent == Muse) {
		parent, ok := idx.Parent(s)
		if !ok {
			return s, fmt.Errorf("parent session of %s is not live", s.ID)
		}
		how := "Forward it to that subagent."
		if s.Agent == Claude {
			how = fmt.Sprintf("Forward it to that subagent with the SendMessage tool (to: %q).", lastSegment(s.NativeID))
		}
		return parent, p.Send(ctx, env, parent, relayText(s, text, how), opts)
	}
	return s, p.Send(ctx, env, s, text, opts)
}

// WaitOptions control WaitForReply.
type WaitOptions struct {
	Timeout  time.Duration
	Interval time.Duration
	// Sent is the message that was delivered; the reply is what follows it.
	Sent string
	// Since ignores matching user messages recorded before this time.
	Since time.Time
}

// sentIndex finds the transcript entry for the delivered message: the last
// user message containing its opening words. Agents may wrap or reflow the
// text (peer framing, relay instructions, tmux joining lines), so the match
// ignores whitespace and only uses a prefix.
func sentIndex(msgs []Message, sent string, since time.Time) int {
	needle := []rune(strings.Join(strings.Fields(sent), " "))
	if len(needle) > 80 {
		needle = needle[:80]
	}
	snippet := string(needle)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "user" || (!m.Time.IsZero() && m.Time.Before(since)) {
			continue
		}
		if strings.Contains(strings.Join(strings.Fields(m.Text), " "), snippet) {
			return i
		}
	}
	return -1
}

// WaitForReply polls until the delivered message appears in the transcript,
// the agent has answered it, and the agent is no longer working. It returns
// the message and everything after it. A message queued behind a running turn
// is not answered until that turn ends, so output from the earlier turn is
// never mistaken for the reply.
func WaitForReply(ctx context.Context, newEnv func() (*Env, error), providers []Provider, target Session, opts WaitOptions) ([]Message, Session, error) {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	deadline := time.Now().Add(opts.Timeout)
	p, err := ProviderFor(providers, target.Agent)
	if err != nil {
		return nil, target, err
	}
	current := target
	stableCount, lastLen := 0, -1
	for {
		env, err := newEnv()
		if err != nil {
			return nil, current, err
		}
		idx := Discover(ctx, env, []Provider{p})
		current, err = idx.Resolve(target.ID)
		if err != nil {
			return nil, target, fmt.Errorf("%s is no longer live", target.ID)
		}
		if msgs, err := p.Transcript(ctx, env, current); err == nil {
			if len(msgs) == lastLen {
				stableCount++
			} else {
				stableCount, lastLen = 0, len(msgs)
			}
			if at := sentIndex(msgs, opts.Sent, opts.Since); at >= 0 {
				fresh := msgs[at:]
				replied := false
				for _, m := range fresh[1:] {
					replied = replied || m.Role == "assistant"
				}
				settled := current.Status == StatusIdle || current.Status == StatusBlocked ||
					(current.Status == StatusUnknown && stableCount >= 3)
				if replied && settled {
					return fresh, current, nil
				}
			}
		}
		if opts.Timeout > 0 && time.Now().After(deadline) {
			return nil, current, fmt.Errorf("timed out after %s waiting for %s (status %s)", opts.Timeout, target.ID, current.Status)
		}
		select {
		case <-ctx.Done():
			return nil, current, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}
