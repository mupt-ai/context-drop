// Package pool is the org-wide agent pool: the types every machine, the
// server, and the CLI share, and the client the CLI uses to reach the server.
// The JSON shapes here are the versioned contract of `--json` output.
package pool

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Agent is one agent in the org: a main session, a subagent, or a workflow agent.
type Agent struct {
	ID      string `json:"id"` // "<machine>/<session>", e.g. "avyay-mbp/cc-49901f7f"
	Machine string `json:"machine"`
	Kind    string `json:"kind"` // claude, codex, pi, opencode
	Role    string `json:"role"` // main, subagent, workflow
	// Parent is who started a subagent; Root is the main session it runs in.
	Parent        string    `json:"parent,omitempty"`
	Root          string    `json:"root,omitempty"`
	AgentType     string    `json:"agent_type,omitempty"`
	Status        string    `json:"status"` // working, idle, blocked, unknown, offline
	Cwd           string    `json:"cwd"`
	Name          string    `json:"name,omitempty"`
	Location      *Location `json:"location,omitempty"`
	LocationLabel string    `json:"location_label,omitempty"`
	StartedAt     string    `json:"started_at,omitempty"`
	LastActive    string    `json:"last_active,omitempty"`
}

// Roles.
const (
	RoleMain     = "main"
	RoleSubagent = "subagent"
	RoleWorkflow = "workflow"
)

// StatusOffline marks agents on a machine that is not connected.
const StatusOffline = "offline"

// Location is where an agent runs: a multiplexer pane, a desktop app, or a
// plain terminal. Exactly one of Mux, App, or Terminal is set.
type Location struct {
	Mux      string `json:"mux,omitempty"`
	Session  *Named `json:"session,omitempty"`
	Window   *Named `json:"window,omitempty"`
	Pane     *Named `json:"pane,omitempty"`
	App      string `json:"app,omitempty"` // claude, codex
	Terminal bool   `json:"terminal,omitempty"`
}

// Named is an ID plus the name people see.
type Named struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// Entry is one item of an agent's transcript.
type Entry struct {
	Type    string `json:"type"` // user, assistant, tool, compacted, recap, error
	Text    string `json:"text,omitempty"`
	Time    string `json:"time,omitempty"`
	From    string `json:"from,omitempty"`
	Name    string `json:"name,omitempty"`
	Input   string `json:"input,omitempty"`
	Trigger string `json:"trigger,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// ReadOptions narrow a read.
type ReadOptions struct {
	Last  int    `json:"last,omitempty"`  // only the last N entries
	All   bool   `json:"all,omitempty"`   // every context run, not just the current one
	After string `json:"after,omitempty"` // only entries newer than this cursor
}

// ReadResult is what `read` returns.
type ReadResult struct {
	Agent   Agent   `json:"agent"`
	Run     int     `json:"run"`
	Runs    int     `json:"runs"`
	Entries []Entry `json:"entries"`
	Cursor  string  `json:"cursor"`
}

// Selector picks agents. An ID wins; otherwise the set fields AND together,
// and All with no other field means every agent.
type Selector struct {
	ID      string `json:"id,omitempty"`
	All     bool   `json:"all,omitempty"`
	Machine string `json:"machine,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// IsZero reports whether the selector names nothing.
func (s Selector) IsZero() bool { return s == Selector{} }

// Matches reports whether an agent fits the selector's filters.
func (s Selector) Matches(a Agent) bool {
	if s.ID != "" {
		return a.ID == s.ID
	}
	if s.Machine != "" && a.Machine != s.Machine {
		return false
	}
	if s.Kind != "" && a.Kind != s.Kind {
		return false
	}
	if s.Cwd != "" && a.Cwd != s.Cwd && !strings.HasPrefix(a.Cwd, strings.TrimRight(s.Cwd, "/")+"/") {
		return false
	}
	return true
}

// SendRequest is the input of `send`.
type SendRequest struct {
	Target Selector `json:"target"`
	Text   string   `json:"text"`
	From   string   `json:"from,omitempty"`
	// Agent is the sending agent's session on the calling machine, if an agent sends.
	Agent string `json:"agent,omitempty"`
	// Slack sends to people on the org's Slack instead of to agents: "#channel", "@person" (a
	// handle, email, or user ID), or "slack:CHANNEL/TS" to reply in a thread. Replies in the
	// thread it starts, and in a DM, come back to the sending agent as prompts.
	Slack string `json:"slack,omitempty"`
}

// IsSlackTarget reports whether a send target names people on Slack rather than an agent.
func IsSlackTarget(t string) bool {
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "@") || strings.HasPrefix(t, "slack:")
}

// SlackReadRequest pulls messages from a Slack conversation.
type SlackReadRequest struct {
	// Target is "#channel", "@person" (their DM with the app), a channel ID, or "slack:CHANNEL/TS" for a thread.
	Target string `json:"target"`
	Limit  int    `json:"limit,omitempty"`
	// Oldest keeps messages after this Slack timestamp.
	Oldest string `json:"oldest,omitempty"`
}

// SlackMessage is one message read from Slack.
type SlackMessage struct {
	TS       string `json:"ts"`
	User     string `json:"user,omitempty"`
	Name     string `json:"name,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Replies  int    `json:"replies,omitempty"`
	// Ref is the message's thread as a send target, "slack:CHANNEL/TS".
	Ref string `json:"ref"`
}

// SlackReadResult is what a Slack read returns, oldest message first.
type SlackReadResult struct {
	Channel  string         `json:"channel"`
	Messages []SlackMessage `json:"messages"`
}

// SendResult is one target's outcome.
type SendResult struct {
	ID        string `json:"id"`
	Delivered bool   `json:"delivered"`
	Via       string `json:"via,omitempty"`
	Error     *Error `json:"error,omitempty"`
}

// SendResponse is what `send` returns.
type SendResponse struct {
	Results []SendResult `json:"results"`
}

// Error is a command failure.
type Error struct {
	Code    string `json:"code"` // offline, not_found, ended, unsupported, timeout, ...
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf builds an Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Frame is one message on a daemon's connection to the server.
//
//	daemon → server: {"type":"agents","agents":[…]}, {"type":"result","id":…,"output"|"error":…}
//	server → daemon: {"type":"call","id":…,"name":"read"|"send","input":…}
type Frame struct {
	Type   string          `json:"type"`
	Agents []Agent         `json:"agents,omitempty"`
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// LocalRead is the input of a "read" call to a daemon.
type LocalRead struct {
	ID string `json:"id"` // the session part of the agent ID
	ReadOptions
}

// LocalSend is the input of a "send" call to a daemon.
type LocalSend struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	From    string `json:"from,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"` // the sending agent's org ID
}

// AskWait is how long the server waits for a machine to answer an ask; the answer itself is given five
// minutes there.
const AskWait = 6 * time.Minute

// AskRequest is the input of `ask`: one question for one agent.
type AskRequest struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	From     string `json:"from,omitempty"`
	// Agent is the asking agent's session on the calling machine, if an agent asks.
	Agent string `json:"agent,omitempty"`
}

// AskResult is the answer to an ask.
type AskResult struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
}

// LocalAsk is the input of an "ask" call to a daemon.
type LocalAsk struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	From     string `json:"from,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"` // the asking agent's org ID
}

var machineName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidMachineName reports whether name can name a machine.
func ValidMachineName(name string) bool { return machineName.MatchString(name) }

// MachineName turns a hostname into a valid machine name: "Avyays-MBP.local" → "avyays-mbp".
func MachineName(host string) string {
	host, _, _ = strings.Cut(strings.ToLower(host), ".")
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	if name == "" {
		name = "machine"
	}
	return name
}

var sessionID = regexp.MustCompile(`^[a-z]{2}-[0-9a-f]{8}$`)

// SplitID splits "<machine>/<session>". ok is false for anything else, so a
// local ID or native ID is never mistaken for an org one.
func SplitID(id string) (machine, session string, ok bool) {
	machine, session, found := strings.Cut(id, "/")
	if !found || !ValidMachineName(machine) || !sessionID.MatchString(session) {
		return "", "", false
	}
	return machine, session, true
}
