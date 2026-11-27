package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Workspace is an org's connection to one Slack workspace. The bot token is stored sealed.
type Workspace struct {
	Org         string    `json:"org"`
	TeamID      string    `json:"team_id"`
	TeamName    string    `json:"team_name"`
	BotUserID   string    `json:"bot_user_id"`
	TokenSealed string    `json:"token_sealed"`
	InstalledBy string    `json:"installed_by"`
	InstalledAt time.Time `json:"installed_at"`
	// Restricted limits agents to linked members. Off by default: anyone in the workspace can reach agents.
	Restricted bool `json:"restricted,omitempty"`
}

// Member is a Slack user linked to an org. Only members' DMs and thread replies reach agents.
type Member struct {
	Org     string    `json:"org"`
	UserID  string    `json:"user_id"`
	Name    string    `json:"name"`
	AddedBy string    `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// Route sends a Slack conversation's replies to agents: a thread (Channel, TS) to every agent that
// posted in it, and a DM (Channel, TS "") to the agent that last opened it.
type Route struct {
	Org     string    `json:"org"`
	Channel string    `json:"channel"`
	TS      string    `json:"ts,omitempty"`
	Agents  []string  `json:"agents"`
	At      time.Time `json:"at"`
}

// state is what the extension saves, under the keys installs, members, and routes.
type state struct {
	Installs []Workspace `json:"installs,omitempty"`
	Members  []Member    `json:"members,omitempty"`
	Routes   []Route     `json:"routes,omitempty"`
}

// routeTTL is how long a route lives after its last use.
const routeTTL = 30 * 24 * time.Hour

// ErrNotInstalled means the org has not connected a Slack workspace.
var ErrNotInstalled = errors.New("this org has not connected Slack; run `context-drop slack install`")

// save writes one org's state; the caller holds e.mu.
func (e *Ext) save(org string) error {
	of := func(o string) bool { return o == org }
	st := state{}
	for _, i := range e.st.Installs {
		if of(i.Org) {
			st.Installs = append(st.Installs, i)
		}
	}
	for _, m := range e.st.Members {
		if of(m.Org) {
			st.Members = append(st.Members, m)
		}
	}
	for _, r := range e.st.Routes {
		if of(r.Org) {
			st.Routes = append(st.Routes, r)
		}
	}
	return e.host.Save(Name, org, st)
}

// load reads every org's saved state.
func (e *Ext) load() error {
	return e.host.Load(Name, func(_ string, data json.RawMessage) error {
		var st state
		if err := json.Unmarshal(data, &st); err != nil {
			return err
		}
		e.st.Installs = append(e.st.Installs, st.Installs...)
		e.st.Members = append(e.st.Members, st.Members...)
		e.st.Routes = append(e.st.Routes, st.Routes...)
		return nil
	})
}

// install connects an org to a workspace, replacing any earlier connection. A workspace belongs to
// one org at a time, since its events must reach exactly one org.
func (e *Ext) install(in Workspace, token string) error {
	if !e.host.HasOrg(in.Org) {
		return fmt.Errorf("org %s not found", in.Org)
	}
	sealed, err := e.host.Seal(token)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, i := range e.st.Installs {
		if i.TeamID == in.TeamID && i.Org != in.Org {
			return fmt.Errorf("Slack workspace %s is already connected to another org", in.TeamName)
		}
	}
	in.TokenSealed = sealed
	in.InstalledAt = time.Now().UTC()
	// Reinstalling keeps the org's choice of who reaches agents.
	if i := slices.IndexFunc(e.st.Installs, func(i Workspace) bool { return i.Org == in.Org }); i >= 0 {
		in.Restricted = e.st.Installs[i].Restricted
	}
	e.st.Installs = append(slices.DeleteFunc(e.st.Installs, func(i Workspace) bool { return i.Org == in.Org }), in)
	return e.save(in.Org)
}

// uninstall forgets an org's workspace, its members, and its routes.
func (e *Ext) uninstall(org string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.st.Installs = slices.DeleteFunc(e.st.Installs, func(i Workspace) bool { return i.Org == org })
	e.st.Members = slices.DeleteFunc(e.st.Members, func(m Member) bool { return m.Org == org })
	e.st.Routes = slices.DeleteFunc(e.st.Routes, func(r Route) bool { return r.Org == org })
	return e.save(org)
}

// workspace returns an org's install and its unsealed bot token.
func (e *Ext) workspace(org string) (Workspace, string, error) {
	e.mu.Lock()
	idx := slices.IndexFunc(e.st.Installs, func(i Workspace) bool { return i.Org == org })
	var w Workspace
	if idx >= 0 {
		w = e.st.Installs[idx]
	}
	e.mu.Unlock()
	if idx < 0 {
		return Workspace{}, "", ErrNotInstalled
	}
	tok, err := e.host.Unseal(w.TokenSealed)
	return w, tok, err
}

// byTeam returns the install for a workspace, for routing its events.
func (e *Ext) byTeam(team string) (Workspace, string, error) {
	e.mu.Lock()
	org := ""
	for _, i := range e.st.Installs {
		if i.TeamID == team {
			org = i.Org
		}
	}
	e.mu.Unlock()
	if org == "" {
		return Workspace{}, "", ErrNotInstalled
	}
	return e.workspace(org)
}

// addMember links a Slack user to an org.
func (e *Ext) addMember(m Member) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m.AddedAt = time.Now().UTC()
	e.st.Members = append(slices.DeleteFunc(e.st.Members, func(x Member) bool { return x.Org == m.Org && x.UserID == m.UserID }), m)
	return e.save(m.Org)
}

// removeMember unlinks a Slack user; it reports whether one was linked.
func (e *Ext) removeMember(org, user string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(e.st.Members)
	e.st.Members = slices.DeleteFunc(e.st.Members, func(x Member) bool { return x.Org == org && x.UserID == user })
	if len(e.st.Members) == n {
		return false, nil
	}
	return true, e.save(org)
}

// members lists an org's linked Slack users.
func (e *Ext) members(org string) []Member {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Member{}
	for _, m := range e.st.Members {
		if m.Org == org {
			out = append(out, m)
		}
	}
	return out
}

// Member returns the linked member with this Slack user ID.
func (e *Ext) Member(org, user string) (Member, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range e.st.Members {
		if m.Org == org && m.UserID == user {
			return m, true
		}
	}
	return Member{}, false
}

// route sends replies in a conversation to an agent. For a thread (ts set), the agent joins everyone
// already in it; for a DM (ts empty), it replaces the agent that opened the DM before.
func (e *Ext) route(org, channel, ts, agent string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().UTC()
	e.st.Routes = slices.DeleteFunc(e.st.Routes, func(r Route) bool { return now.Sub(r.At) > routeTTL })
	for i := range e.st.Routes {
		r := &e.st.Routes[i]
		if r.Org != org || r.Channel != channel || r.TS != ts {
			continue
		}
		r.At = now
		if ts == "" {
			r.Agents = []string{agent}
		} else if !slices.Contains(r.Agents, agent) {
			r.Agents = append(r.Agents, agent)
		}
		return e.save(org)
	}
	e.st.Routes = append(e.st.Routes, Route{Org: org, Channel: channel, TS: ts, Agents: []string{agent}, At: now})
	return e.save(org)
}

// routed returns the agents a conversation's replies go to.
func (e *Ext) routed(org, channel, ts string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.st.Routes {
		if r.Org == org && r.Channel == channel && r.TS == ts && time.Since(r.At) <= routeTTL {
			return slices.Clone(r.Agents)
		}
	}
	return nil
}

// Connect installs a workspace for an org without the OAuth round trip, for tests and tools.
func (e *Ext) Connect(w Workspace, token string) error { return e.install(w, token) }

// setRestricted turns the linked-members-only rule on or off for an org's workspace.
func (e *Ext) setRestricted(org string, on bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := slices.IndexFunc(e.st.Installs, func(i Workspace) bool { return i.Org == org })
	if idx < 0 {
		return ErrNotInstalled
	}
	e.st.Installs[idx].Restricted = on
	return e.save(org)
}
