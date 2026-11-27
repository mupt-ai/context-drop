package slack

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"contextdrop.dev/context-drop/internal/extensions"
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

// get reads an org's state.
func (e *Ext) get(org string) (state, error) {
	var st state
	_, err := e.host.Get(Name, org, &st)
	return st, err
}

// update changes an org's state, with no other server changing it in between.
func (e *Ext) update(org string, fn func(*state) error) error {
	var st state
	return e.host.Update(context.Background(), Name, org, &st, func() error { return fn(&st) })
}

// index makes sure every installed workspace is claimed by its org, for state saved before workspaces
// were claimed.
func (e *Ext) index() error {
	return e.host.Each(Name, func(org string, data json.RawMessage) error {
		var st state
		if err := json.Unmarshal(data, &st); err != nil {
			return err
		}
		for _, i := range st.Installs {
			if err := e.host.Claim(Name, i.TeamID, org); err != nil {
				e.host.Log().Warn("slack workspace held by two orgs", "team", i.TeamID, "org", org, "err", err)
			}
		}
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
	if err := e.host.Claim(Name, in.TeamID, in.Org); errors.Is(err, extensions.ErrTaken) {
		return fmt.Errorf("Slack workspace %s is already connected to another org", in.TeamName)
	} else if err != nil {
		return err
	}
	var old string
	err = e.update(in.Org, func(st *state) error {
		if len(st.Installs) > 0 {
			old = st.Installs[0].TeamID
			in.Restricted = st.Installs[0].Restricted
		}
		in.TokenSealed = sealed
		in.InstalledAt = time.Now().UTC()
		st.Installs = []Workspace{in}
		return nil
	})
	if err != nil {
		// Free the workspace unless the org's saved install uses it (another install may have won).
		if cur, _, werr := e.workspace(in.Org); errors.Is(werr, ErrNotInstalled) || (werr == nil && cur.TeamID != in.TeamID) {
			_ = e.host.Release(Name, in.TeamID, in.Org)
		}
		return err
	}
	if old != "" && old != in.TeamID {
		_ = e.host.Release(Name, old, in.Org)
	}
	return nil
}

// uninstall forgets an org's workspace, its members, and its routes.
func (e *Ext) uninstall(org string) error {
	var teams []string
	err := e.update(org, func(st *state) error {
		for _, i := range st.Installs {
			teams = append(teams, i.TeamID)
		}
		*st = state{}
		return nil
	})
	if err != nil {
		return err
	}
	for _, t := range teams {
		_ = e.host.Release(Name, t, org)
	}
	return nil
}

// workspace returns an org's install and its unsealed bot token.
func (e *Ext) workspace(org string) (Workspace, string, error) {
	st, err := e.get(org)
	if err != nil {
		return Workspace{}, "", err
	}
	if len(st.Installs) == 0 {
		return Workspace{}, "", ErrNotInstalled
	}
	w := st.Installs[0]
	tok, err := e.host.Unseal(w.TokenSealed)
	return w, tok, err
}

// byTeam returns the install for a workspace, for routing its events.
func (e *Ext) byTeam(team string) (Workspace, string, error) {
	org, ok, err := e.host.Owner(Name, team)
	if err != nil {
		return Workspace{}, "", err
	}
	if !ok {
		return Workspace{}, "", ErrNotInstalled
	}
	w, tok, err := e.workspace(org)
	if err == nil && w.TeamID != team {
		return Workspace{}, "", ErrNotInstalled
	}
	return w, tok, err
}

// addMember links a Slack user to an org.
func (e *Ext) addMember(m Member) error {
	m.AddedAt = time.Now().UTC()
	return e.update(m.Org, func(st *state) error {
		st.Members = append(slices.DeleteFunc(st.Members, func(x Member) bool { return x.UserID == m.UserID }), m)
		return nil
	})
}

// errNoChange makes update write nothing.
var errNoChange = errors.New("no change")

// removeMember unlinks a Slack user; it reports whether one was linked.
func (e *Ext) removeMember(org, user string) (bool, error) {
	err := e.update(org, func(st *state) error {
		n := len(st.Members)
		st.Members = slices.DeleteFunc(st.Members, func(x Member) bool { return x.UserID == user })
		if len(st.Members) == n {
			return errNoChange
		}
		return nil
	})
	if errors.Is(err, errNoChange) {
		return false, nil
	}
	return err == nil, err
}

// members lists an org's linked Slack users.
func (e *Ext) members(org string) []Member {
	st, _ := e.get(org)
	if st.Members == nil {
		return []Member{}
	}
	return st.Members
}

// Member returns the linked member with this Slack user ID.
func (e *Ext) Member(org, user string) (Member, bool) {
	for _, m := range e.members(org) {
		if m.UserID == user {
			return m, true
		}
	}
	return Member{}, false
}

// route sends replies in a conversation to an agent. For a thread (ts set), the agent joins everyone
// already in it; for a DM (ts empty), it replaces the agent that opened the DM before.
func (e *Ext) route(org, channel, ts, agent string) error {
	return e.update(org, func(st *state) error {
		now := time.Now().UTC()
		st.Routes = slices.DeleteFunc(st.Routes, func(r Route) bool { return now.Sub(r.At) > routeTTL })
		for i := range st.Routes {
			r := &st.Routes[i]
			if r.Channel != channel || r.TS != ts {
				continue
			}
			r.At = now
			if ts == "" {
				r.Agents = []string{agent}
			} else if !slices.Contains(r.Agents, agent) {
				r.Agents = append(r.Agents, agent)
			}
			return nil
		}
		st.Routes = append(st.Routes, Route{Org: org, Channel: channel, TS: ts, Agents: []string{agent}, At: now})
		return nil
	})
}

// routed returns the agents a conversation's replies go to.
func (e *Ext) routed(org, channel, ts string) []string {
	st, _ := e.get(org)
	for _, r := range st.Routes {
		if r.Channel == channel && r.TS == ts && time.Since(r.At) <= routeTTL {
			return r.Agents
		}
	}
	return nil
}

// Connect installs a workspace for an org without the OAuth round trip, for tests and tools.
func (e *Ext) Connect(w Workspace, token string) error { return e.install(w, token) }

// setRestricted turns the linked-members-only rule on or off for an org's workspace.
func (e *Ext) setRestricted(org string, on bool) error {
	return e.update(org, func(st *state) error {
		if len(st.Installs) == 0 {
			return ErrNotInstalled
		}
		st.Installs[0].Restricted = on
		return nil
	})
}
