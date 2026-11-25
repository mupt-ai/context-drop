package orgserver

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// SlackInstall is an org's connection to one Slack workspace. The bot token is stored encrypted.
type SlackInstall struct {
	Org         string    `json:"org"`
	TeamID      string    `json:"team_id"`
	TeamName    string    `json:"team_name"`
	BotUserID   string    `json:"bot_user_id"`
	TokenSealed string    `json:"token_sealed"`
	InstalledBy string    `json:"installed_by"`
	InstalledAt time.Time `json:"installed_at"`
}

// SlackMember is a Slack user linked to an org. Only members' DMs and thread replies reach agents.
type SlackMember struct {
	Org     string    `json:"org"`
	UserID  string    `json:"user_id"`
	Name    string    `json:"name"`
	AddedBy string    `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// SlackRoute sends a Slack conversation's replies to agents: a thread (Channel, TS) to every agent
// that posted in it, and a DM (Channel, TS "") to the agent that last opened it.
type SlackRoute struct {
	Org     string    `json:"org"`
	Channel string    `json:"channel"`
	TS      string    `json:"ts,omitempty"`
	Agents  []string  `json:"agents"`
	At      time.Time `json:"at"`
}

// slackRouteTTL is how long a route lives after its last use.
const slackRouteTTL = 30 * 24 * time.Hour

// ErrSlackNotInstalled means the org has not connected a Slack workspace.
var ErrSlackNotInstalled = errors.New("this org has not connected Slack; run `context-drop slack install`")

// SetSlackKey sets the key bot tokens are sealed with. Without one, Slack is off.
func (s *Store) SetSlackKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		s.slackKey = nil
		return
	}
	sum := sha256.Sum256([]byte(key))
	s.slackKey = sum[:]
}

func (s *Store) seal(plain string) (string, error) {
	if s.slackKey == nil {
		return "", errors.New("no Slack token key is configured (CONTEXT_DROP_SLACK_KEY)")
	}
	block, err := aes.NewCipher(s.slackKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) unseal(sealed string) (string, error) {
	if s.slackKey == nil {
		return "", errors.New("no Slack token key is configured (CONTEXT_DROP_SLACK_KEY)")
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.slackKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("sealed token is too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot unseal the Slack token; was CONTEXT_DROP_SLACK_KEY changed?")
	}
	return string(plain), nil
}

// InstallSlack connects an org to a workspace, replacing any earlier connection. A workspace belongs
// to one org at a time, since its events must reach exactly one org.
func (s *Store) InstallSlack(in SlackInstall, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasOrg(in.Org) {
		return fmt.Errorf("org %s %w", in.Org, ErrNotFound)
	}
	for _, i := range s.st.SlackInstalls {
		if i.TeamID == in.TeamID && i.Org != in.Org {
			return fmt.Errorf("Slack workspace %s is already connected to another org", in.TeamName)
		}
	}
	sealed, err := s.seal(token)
	if err != nil {
		return err
	}
	in.TokenSealed = sealed
	in.InstalledAt = time.Now().UTC()
	kept := s.st.SlackInstalls[:0]
	for _, i := range s.st.SlackInstalls {
		if i.Org != in.Org {
			kept = append(kept, i)
		}
	}
	s.st.SlackInstalls = append(kept, in)
	return s.save()
}

// UninstallSlack forgets an org's workspace, its members, and its routes.
func (s *Store) UninstallSlack(org string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.SlackInstalls = without(s.st.SlackInstalls, func(i SlackInstall) bool { return i.Org == org })
	s.st.SlackMembers = without(s.st.SlackMembers, func(m SlackMember) bool { return m.Org == org })
	s.st.SlackRoutes = without(s.st.SlackRoutes, func(r SlackRoute) bool { return r.Org == org })
	return s.save()
}

func without[T any](list []T, drop func(T) bool) []T {
	kept := list[:0]
	for _, x := range list {
		if !drop(x) {
			kept = append(kept, x)
		}
	}
	return kept
}

// Slack returns an org's install and its unsealed bot token.
func (s *Store) Slack(org string) (SlackInstall, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.st.SlackInstalls {
		if i.Org == org {
			tok, err := s.unseal(i.TokenSealed)
			return i, tok, err
		}
	}
	return SlackInstall{}, "", ErrSlackNotInstalled
}

// SlackByTeam returns the install for a workspace, for routing its events.
func (s *Store) SlackByTeam(team string) (SlackInstall, string, error) {
	s.mu.Lock()
	org := ""
	for _, i := range s.st.SlackInstalls {
		if i.TeamID == team {
			org = i.Org
		}
	}
	s.mu.Unlock()
	if org == "" {
		return SlackInstall{}, "", ErrSlackNotInstalled
	}
	return s.Slack(org)
}

// AddSlackMember links a Slack user to an org.
func (s *Store) AddSlackMember(m SlackMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.AddedAt = time.Now().UTC()
	s.st.SlackMembers = without(s.st.SlackMembers, func(x SlackMember) bool { return x.Org == m.Org && x.UserID == m.UserID })
	s.st.SlackMembers = append(s.st.SlackMembers, m)
	return s.save()
}

// RemoveSlackMember unlinks a Slack user; it reports whether one was linked.
func (s *Store) RemoveSlackMember(org, user string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.SlackMembers)
	s.st.SlackMembers = without(s.st.SlackMembers, func(x SlackMember) bool { return x.Org == org && x.UserID == user })
	if len(s.st.SlackMembers) == n {
		return false, nil
	}
	return true, s.save()
}

// SlackMembers lists an org's linked Slack users.
func (s *Store) SlackMembers(org string) []SlackMember {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SlackMember
	for _, m := range s.st.SlackMembers {
		if m.Org == org {
			out = append(out, m)
		}
	}
	return out
}

// SlackMember returns the linked member with this Slack user ID.
func (s *Store) SlackMember(org, user string) (SlackMember, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.st.SlackMembers {
		if m.Org == org && m.UserID == user {
			return m, true
		}
	}
	return SlackMember{}, false
}

// RouteSlack sends replies in a conversation to an agent. For a thread (ts set), the agent joins
// everyone already in it; for a DM (ts empty), it replaces the agent that opened the DM before.
func (s *Store) RouteSlack(org, channel, ts, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.st.SlackRoutes = without(s.st.SlackRoutes, func(r SlackRoute) bool { return now.Sub(r.At) > slackRouteTTL })
	for i := range s.st.SlackRoutes {
		r := &s.st.SlackRoutes[i]
		if r.Org != org || r.Channel != channel || r.TS != ts {
			continue
		}
		r.At = now
		if ts == "" {
			r.Agents = []string{agent}
		} else if !contains(r.Agents, agent) {
			r.Agents = append(r.Agents, agent)
		}
		return s.save()
	}
	s.st.SlackRoutes = append(s.st.SlackRoutes, SlackRoute{Org: org, Channel: channel, TS: ts, Agents: []string{agent}, At: now})
	return s.save()
}

// SlackRouted returns the agents a conversation's replies go to.
func (s *Store) SlackRouted(org, channel, ts string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.st.SlackRoutes {
		if r.Org == org && r.Channel == channel && r.TS == ts && time.Since(r.At) <= slackRouteTTL {
			return append([]string(nil), r.Agents...)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Store) hasOrg(id string) bool {
	for _, o := range s.st.Orgs {
		if o.ID == id {
			return true
		}
	}
	return false
}
