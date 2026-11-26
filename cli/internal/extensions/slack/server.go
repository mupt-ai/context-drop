package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// Name is the extension's name and the capability a mod needs to use it.
const Name = "slack"

// Ext is the server's Slack app. Each org installs it into its own workspace.
type Ext struct {
	ClientID      string
	ClientSecret  string
	SigningSecret string
	// API is the Slack Web API base; empty means slack.com.
	API string

	host extensions.Host
	mu   sync.Mutex // guards st
	st   state

	cacheMu sync.Mutex
	names   map[string]string    // org|kind|key → value: channel names → IDs, user IDs → names
	events  map[string]time.Time // event IDs already handled, since Slack retries
}

// Name implements extensions.Extension.
func (e *Ext) Name() string { return Name }

// Owns reports whether a target is a Slack one: "#channel", "@person", or "slack:CHANNEL/TS".
func (e *Ext) Owns(target string) bool { return IsTarget(target) }

// IsTarget reports whether a send target names people on Slack.
func IsTarget(t string) bool {
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "@") || strings.HasPrefix(t, "slack:")
}

func (e *Ext) redirect() string {
	return strings.TrimRight(e.host.PublicURL(), "/") + "/v1/slack/oauth"
}

// Register implements extensions.Extension.
func (e *Ext) Register(mux *http.ServeMux, h extensions.Host) error {
	e.host = h
	if err := h.Load(Name, &e.st); err != nil {
		return err
	}
	mux.HandleFunc("GET /v1/slack/oauth", e.handleOAuth)
	mux.HandleFunc("GET /v1/slack/link", e.handleLink)
	mux.HandleFunc("POST /v1/slack/events", e.handleEvents)
	mux.HandleFunc("POST /v1/{org}/slack/connect", h.Authed(e.handleConnect))
	mux.HandleFunc("POST /v1/{org}/slack/install", h.Authed(e.handleInstall))
	mux.HandleFunc("POST /v1/{org}/slack/uninstall", h.Authed(e.handleUninstall))
	mux.HandleFunc("POST /v1/{org}/slack/status", h.Authed(e.handleStatus))
	mux.HandleFunc("POST /v1/{org}/slack/members", h.Authed(e.handleMembers))
	mux.HandleFunc("POST /v1/{org}/slack/read", h.Authed(e.handleRead))
	mux.HandleFunc("POST /v1/{org}/slack/api", h.Authed(e.handleAPI))
	return nil
}

func (e *Ext) cached(org, kind, key string) (string, bool) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	v, ok := e.names[org+"|"+kind+"|"+key]
	return v, ok
}

func (e *Ext) remember(org, kind, key, value string) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	if e.names == nil {
		e.names = map[string]string{}
	}
	e.names[org+"|"+kind+"|"+key] = value
}

// seenEvent reports whether an event was handled already, and marks it handled.
func (e *Ext) seenEvent(id string) bool {
	if id == "" {
		return false
	}
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	if e.events == nil {
		e.events = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range e.events {
		if now.Sub(t) > time.Hour {
			delete(e.events, k)
		}
	}
	if _, ok := e.events[id]; ok {
		return true
	}
	e.events[id] = now
	return false
}

// client is the org's Web API client.
func (e *Ext) client(org string) (Client, Workspace, *pool.Error) {
	in, tok, err := e.workspace(org)
	if errors.Is(err, ErrNotInstalled) {
		return Client{}, in, pool.Errorf("not_found", "%v", err)
	}
	if err != nil {
		return Client{}, in, pool.Errorf("internal", "%v", err)
	}
	return Client{Token: tok, Base: e.API}, in, nil
}

func asPoolError(err error) *pool.Error {
	var se *Error
	if errors.As(err, &se) {
		code := "bad_request"
		switch se.Code {
		case "channel_not_found", "users_not_found", "user_not_found", "thread_not_found", "message_not_found":
			code = "not_found"
		case "not_in_channel", "missing_scope", "not_allowed_token_type", "restricted_action":
			code = "forbidden"
		}
		return pool.Errorf(code, "%v", err)
	}
	var pe *pool.Error
	if errors.As(err, &pe) {
		return pe
	}
	return pool.Errorf("internal", "%v", err)
}

// ── install ─────────────────────────────────────────────────────────────────

// state signs what an OAuth round trip is for ("install" a workspace, or "connect" a person), the org
// and machine that asked, and when the request expires.
func (e *Ext) state(kind, org, machine string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(kind + "\n" + org + "\n" + machine + "\n" + strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, []byte(e.ClientSecret))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (e *Ext) checkState(state string) (kind, org, machine string, ok bool) {
	payload, sig, found := strings.Cut(state, ".")
	if !found {
		return "", "", "", false
	}
	mac := hmac.New(sha256.New, []byte(e.ClientSecret))
	mac.Write([]byte(payload))
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return "", "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(string(raw), "\n")
	if len(parts) != 4 {
		return "", "", "", false
	}
	exp, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || time.Now().After(time.Unix(exp, 0)) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func (e *Ext) handleInstall(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	state := e.state("install", c.Org, c.Machine, time.Now().Add(30*time.Minute))
	extensions.WriteJSON(w, http.StatusOK, map[string]string{"url": AuthorizeURL(e.ClientID, e.redirect(), state)})
}

func (e *Ext) handleOAuth(w http.ResponseWriter, r *http.Request) {
	page := func(status int, title, body string) {
		extensions.Page(w, status, title, "<p>"+html.EscapeString(body)+"</p>")
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		page(http.StatusBadRequest, "Not connected", "Slack said: "+msg)
		return
	}
	kind, org, machine, ok := e.checkState(r.URL.Query().Get("state"))
	if !ok {
		page(http.StatusBadRequest, "Link expired", "Run `context-drop slack install` again for a fresh link.")
		return
	}
	if kind == "connect" {
		e.finishConnect(w, r, org, machine)
		return
	}
	got, err := Exchange(r.Context(), e.API, e.ClientID, e.ClientSecret, r.URL.Query().Get("code"), e.redirect())
	if err != nil {
		page(http.StatusBadGateway, "Not connected", err.Error())
		return
	}
	in := Workspace{Org: org, TeamID: got.TeamID, TeamName: got.TeamName, BotUserID: got.BotUserID, InstalledBy: machine}
	if err := e.install(in, got.Token); err != nil {
		page(http.StatusConflict, "Not connected", err.Error())
		return
	}
	e.host.Log().Info("slack connected", "org", org, "team", got.TeamID, "by", machine)
	// Whoever approved the install is linked too, so they can answer agents right away.
	linked := ""
	if got.InstallerID != "" {
		name := e.userName(r.Context(), Client{Token: got.Token, Base: e.API}, org, got.InstallerID)
		if err := e.addMember(Member{Org: org, UserID: got.InstallerID, Name: name, AddedBy: machine}); err == nil {
			linked = " You (" + name + ") are linked, so agents can reach you."
		}
	}
	page(http.StatusOK, "Slack connected", fmt.Sprintf("Workspace %s is connected to org %s.%s Everyone else links themselves with `context-drop slack connect`.", got.TeamName, org, linked))
}

func (e *Ext) handleUninstall(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	if err := e.uninstall(c.Org); err != nil {
		extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	extensions.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Status is what `slack status` returns.
type Status struct {
	Configured bool     `json:"configured"`
	Installed  bool     `json:"installed"`
	Team       string   `json:"team,omitempty"`
	TeamID     string   `json:"team_id,omitempty"`
	BotUserID  string   `json:"bot_user_id,omitempty"`
	Members    []Member `json:"members"`
}

func (e *Ext) handleStatus(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	out := Status{Configured: true, Members: e.members(c.Org)}
	if in, _, err := e.workspace(c.Org); err == nil {
		out.Installed, out.Team, out.TeamID, out.BotUserID = true, in.TeamName, in.TeamID, in.BotUserID
	}
	extensions.WriteJSON(w, http.StatusOK, out)
}

// handleMembers links and unlinks the Slack users whose DMs and thread replies reach agents. Only
// machines may change it: who can prompt the org's agents is not a mod's call.
func (e *Ext) handleMembers(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	var in struct {
		Op   string `json:"op"` // add, remove, list
		User string `json:"user"`
	}
	if !extensions.Decode(w, r, &in) {
		return
	}
	if in.Op == "" || in.Op == "list" {
		extensions.WriteJSON(w, http.StatusOK, e.members(c.Org))
		return
	}
	cl, _, perr := e.client(c.Org)
	if perr != nil {
		extensions.Fail(w, perr)
		return
	}
	id, name, err := e.findUser(r.Context(), cl, c.Org, in.User)
	if err != nil {
		extensions.Fail(w, asPoolError(err))
		return
	}
	switch in.Op {
	case "add":
		m := Member{Org: c.Org, UserID: id, Name: name, AddedBy: c.Machine}
		if err := e.addMember(m); err != nil {
			extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
		extensions.WriteJSON(w, http.StatusOK, m)
	case "remove":
		ok, err := e.removeMember(c.Org, id)
		if err != nil {
			extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
		if !ok {
			extensions.WriteError(w, http.StatusNotFound, "not_found", name+" is not linked to this org")
			return
		}
		extensions.WriteJSON(w, http.StatusOK, map[string]string{"user_id": id, "name": name})
	default:
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", "op is add, remove, or list")
	}
}

// ── finding people and channels ─────────────────────────────────────────────

var (
	userID    = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)
	channelID = regexp.MustCompile(`^[CGD][A-Z0-9]{6,}$`)
)

// findUser resolves "@handle", "@email", or a user ID to a user ID and display name.
func (e *Ext) findUser(ctx context.Context, cl Client, org, who string) (id, name string, err error) {
	who = strings.TrimPrefix(strings.TrimSpace(who), "@")
	if who == "" {
		return "", "", pool.Errorf("bad_request", "name a Slack user: @handle, @email, or a user ID")
	}
	if userID.MatchString(who) {
		return who, e.userName(ctx, cl, org, who), nil
	}
	if strings.Contains(who, "@") {
		raw, err := cl.Get(ctx, "users.lookupByEmail", url.Values{"email": {who}})
		if err != nil {
			return "", "", err
		}
		var out struct{ User user }
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", "", err
		}
		return out.User.ID, out.User.display(), nil
	}
	if id, ok := e.cached(org, "handle", strings.ToLower(who)); ok {
		return id, e.userName(ctx, cl, org, id), nil
	}
	cursor := ""
	for {
		q := url.Values{"limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		raw, err := cl.Get(ctx, "users.list", q)
		if err != nil {
			return "", "", err
		}
		var page struct {
			Members  []user `json:"members"`
			Metadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", "", err
		}
		for _, u := range page.Members {
			if u.Deleted {
				continue
			}
			for _, n := range []string{u.Name, u.Profile.DisplayName, u.Profile.RealName} {
				if n != "" && strings.EqualFold(n, who) {
					e.remember(org, "handle", strings.ToLower(who), u.ID)
					e.remember(org, "user", u.ID, u.display())
					return u.ID, u.display(), nil
				}
			}
		}
		if cursor = page.Metadata.NextCursor; cursor == "" {
			return "", "", pool.Errorf("not_found", "no Slack user named %s", who)
		}
	}
}

type user struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
	Profile struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

func (u user) display() string {
	for _, n := range []string{u.Profile.DisplayName, u.Profile.RealName, u.Name} {
		if n != "" {
			return n
		}
	}
	return u.ID
}

// userName is a user's display name, or their ID when it cannot be looked up.
func (e *Ext) userName(ctx context.Context, cl Client, org, id string) string {
	if n, ok := e.cached(org, "user", id); ok {
		return n
	}
	raw, err := cl.Get(ctx, "users.info", url.Values{"user": {id}})
	if err != nil {
		return id
	}
	var out struct{ User user }
	if json.Unmarshal(raw, &out) != nil {
		return id
	}
	e.remember(org, "user", id, out.User.display())
	return out.User.display()
}

// findChannel resolves "#name" or a channel ID to a channel ID.
func (e *Ext) findChannel(ctx context.Context, cl Client, org, name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "#")
	if channelID.MatchString(name) {
		return name, nil
	}
	if id, ok := e.cached(org, "channel", name); ok {
		return id, nil
	}
	cursor := ""
	for {
		q := url.Values{"limit": {"1000"}, "exclude_archived": {"true"}, "types": {"public_channel,private_channel"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		raw, err := cl.Get(ctx, "conversations.list", q)
		if err != nil {
			return "", err
		}
		var page struct {
			Channels []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channels"`
			Metadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", err
		}
		for _, ch := range page.Channels {
			e.remember(org, "channel", ch.Name, ch.ID)
			if ch.Name == name {
				return ch.ID, nil
			}
		}
		if cursor = page.Metadata.NextCursor; cursor == "" {
			return "", pool.Errorf("not_found", "no Slack channel #%s the app can see (invite it, or check the name)", name)
		}
	}
}

// conversation is a resolved Slack target.
type conversation struct {
	channel string
	thread  string // the thread's root ts, if the target is a thread
	dm      bool
}

// resolve turns a send or read target into a conversation: "#channel", "@person" (their DM with the
// app), a channel ID, or "slack:CHANNEL[/TS]".
func (e *Ext) resolve(ctx context.Context, cl Client, org, target string) (conversation, error) {
	target = strings.TrimSpace(target)
	switch {
	case strings.HasPrefix(target, "slack:"):
		ch, ts, _ := strings.Cut(strings.TrimPrefix(target, "slack:"), "/")
		if !channelID.MatchString(ch) {
			return conversation{}, pool.Errorf("bad_request", "%q is not a Slack thread like slack:C0123ABCD/1700000000.000100", target)
		}
		return conversation{channel: ch, thread: ts, dm: strings.HasPrefix(ch, "D")}, nil
	case strings.HasPrefix(target, "@"):
		id, _, err := e.findUser(ctx, cl, org, target)
		if err != nil {
			return conversation{}, err
		}
		if ch, ok := e.cached(org, "dm", id); ok {
			return conversation{channel: ch, dm: true}, nil
		}
		raw, err := cl.Call(ctx, "conversations.open", map[string]any{"users": id})
		if err != nil {
			return conversation{}, err
		}
		var out struct {
			Channel struct {
				ID string `json:"id"`
			} `json:"channel"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return conversation{}, err
		}
		e.remember(org, "dm", id, out.Channel.ID)
		return conversation{channel: out.Channel.ID, dm: true}, nil
	default:
		ch, err := e.findChannel(ctx, cl, org, target)
		return conversation{channel: ch, dm: strings.HasPrefix(ch, "D")}, err
	}
}

func ref(channel, ts string) string {
	if ts == "" {
		return "slack:" + channel
	}
	return "slack:" + channel + "/" + ts
}

// ── send and read ───────────────────────────────────────────────────────────

// Send posts a message for people. When an agent sends, replies in the thread it starts (or
// continues), and in a DM it opens, are routed back to that agent.
func (e *Ext) Send(ctx context.Context, c extensions.Caller, target, text, from, replyTo string) (pool.SendResult, *pool.Error) {
	cl, _, perr := e.client(c.Org)
	if perr != nil {
		return pool.SendResult{}, perr
	}
	conv, err := e.resolve(ctx, cl, c.Org, target)
	if err != nil {
		return pool.SendResult{}, asPoolError(err)
	}
	if replyTo != "" {
		text += "\n\n_— agent `" + replyTo + "`; reply in this thread to answer_"
	} else {
		text += "\n\n_— " + from + "_"
	}
	args := map[string]any{"channel": conv.channel, "text": text, "unfurl_links": false, "unfurl_media": false}
	if conv.thread != "" {
		args["thread_ts"] = conv.thread
	}
	raw, err := cl.Call(ctx, "chat.postMessage", args)
	var se *Error
	if errors.As(err, &se) && se.Code == "not_in_channel" && strings.HasPrefix(conv.channel, "C") {
		// A public channel the app is not in yet: join it and try once more.
		if _, jerr := cl.Call(ctx, "conversations.join", map[string]any{"channel": conv.channel}); jerr == nil {
			raw, err = cl.Call(ctx, "chat.postMessage", args)
		}
	}
	if err != nil {
		return pool.SendResult{}, asPoolError(err)
	}
	var posted struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := json.Unmarshal(raw, &posted); err != nil {
		return pool.SendResult{}, pool.Errorf("internal", "%v", err)
	}
	root := conv.thread
	if root == "" {
		root = posted.TS
	}
	if replyTo != "" {
		if err := e.route(c.Org, posted.Channel, root, replyTo); err != nil {
			e.host.Log().Warn("slack route", "err", err)
		}
		if conv.dm {
			if err := e.route(c.Org, posted.Channel, "", replyTo); err != nil {
				e.host.Log().Warn("slack route", "err", err)
			}
		}
	}
	return pool.SendResult{ID: ref(posted.Channel, root), Delivered: true, Via: Name, TS: posted.TS}, nil
}

type message struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
}

func (e *Ext) handleRead(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.Allowed(w, c, Name) {
		return
	}
	var in pool.SlackReadRequest
	if !extensions.Decode(w, r, &in) {
		return
	}
	cl, inst, perr := e.client(c.Org)
	if perr != nil {
		extensions.Fail(w, perr)
		return
	}
	out, err := e.read(r.Context(), cl, inst, c.Org, in)
	if err != nil {
		extensions.Fail(w, asPoolError(err))
		return
	}
	extensions.WriteJSON(w, http.StatusOK, out)
}

func (e *Ext) read(ctx context.Context, cl Client, inst Workspace, org string, in pool.SlackReadRequest) (pool.SlackReadResult, error) {
	conv, err := e.resolve(ctx, cl, org, in.Target)
	if err != nil {
		return pool.SlackReadResult{}, err
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := url.Values{"channel": {conv.channel}, "limit": {strconv.Itoa(limit)}}
	if in.Oldest != "" {
		q.Set("oldest", in.Oldest)
	}
	method := "conversations.history"
	if conv.thread != "" {
		method = "conversations.replies"
		q.Set("ts", conv.thread)
	}
	raw, err := cl.Get(ctx, method, q)
	if err != nil {
		return pool.SlackReadResult{}, err
	}
	var page struct {
		Messages []message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return pool.SlackReadResult{}, err
	}
	msgs := make([]pool.SlackMessage, 0, len(page.Messages))
	for _, m := range page.Messages {
		root := m.ThreadTS
		if root == "" {
			root = m.TS
		}
		sm := pool.SlackMessage{TS: m.TS, User: m.User, Text: m.Text, ThreadTS: m.ThreadTS, Replies: m.ReplyCount, Ref: ref(conv.channel, root),
			Bot: m.BotID != "" || (m.User != "" && m.User == inst.BotUserID)}
		if m.User != "" {
			sm.Name = e.userName(ctx, cl, org, m.User)
		}
		msgs = append(msgs, sm)
	}
	sort.Slice(msgs, func(i, j int) bool { return tsValue(msgs[i].TS) < tsValue(msgs[j].TS) })
	return pool.SlackReadResult{Channel: conv.channel, Messages: msgs}, nil
}

func tsValue(ts string) float64 {
	f, _ := strconv.ParseFloat(ts, 64)
	return f
}

// methods are the Web API method families a mod may call. Anything that administers the workspace or
// the app's own install is left out.
var methods = []string{"chat.", "conversations.", "reactions.", "pins.", "bookmarks.", "users.info", "users.list", "users.lookupByEmail", "users.profile.get", "team.info", "files.info", "files.list"}

// MethodAllowed reports whether a mod may call a Web API method.
func MethodAllowed(m string) bool {
	if m == "chat.delete" || strings.HasPrefix(m, "conversations.kick") || m == "conversations.archive" || m == "conversations.rename" {
		return false
	}
	for _, p := range methods {
		if strings.HasSuffix(p, ".") && strings.HasPrefix(m, p) || m == p {
			return true
		}
	}
	return false
}

// handleAPI calls one Slack Web API method with the org's bot token, for mods and tools.
func (e *Ext) handleAPI(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.Allowed(w, c, Name) {
		return
	}
	var in struct {
		Method string         `json:"method"`
		Args   map[string]any `json:"args"`
	}
	if !extensions.Decode(w, r, &in) {
		return
	}
	if !MethodAllowed(in.Method) {
		extensions.WriteError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("Slack method %q is not available (allowed: %s)", in.Method, strings.Join(methods, ", ")))
		return
	}
	cl, _, perr := e.client(c.Org)
	if perr != nil {
		extensions.Fail(w, perr)
		return
	}
	if in.Args == nil {
		in.Args = map[string]any{}
	}
	raw, err := cl.Call(r.Context(), in.Method, in.Args)
	if err != nil {
		extensions.Fail(w, asPoolError(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// ── events: people answering agents ─────────────────────────────────────────

type event struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Text        string `json:"text"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
}

func (e *Ext) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := Verify(e.SigningSecret, r.Header, body, time.Now()); err != nil {
		extensions.WriteError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	var env struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		TeamID    string `json:"team_id"`
		EventID   string `json:"event_id"`
		Event     event  `json:"event"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if env.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, env.Challenge)
		return
	}
	// Slack wants an answer within three seconds, and retries otherwise; deliver in the background.
	w.WriteHeader(http.StatusOK)
	if env.Type != "event_callback" || e.seenEvent(env.EventID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		e.deliver(ctx, env.TeamID, env.Event)
	}()
}

// deliver pushes a person's message to the agents waiting on it: a reply in a thread an agent posted
// in goes to every agent in that thread, and a DM goes to the agent that last opened the DM. Anything
// else in a channel stays there, for agents to pull with a read.
func (e *Ext) deliver(ctx context.Context, team string, ev event) {
	if ev.Type != "message" || ev.BotID != "" || ev.User == "" {
		return
	}
	if ev.Subtype != "" && ev.Subtype != "file_share" && ev.Subtype != "thread_broadcast" {
		return
	}
	inst, tok, err := e.byTeam(team)
	if err != nil || ev.User == inst.BotUserID {
		return
	}
	org := inst.Org
	cl := Client{Token: tok, Base: e.API}
	threaded := ev.ThreadTS != "" && ev.ThreadTS != ev.TS
	dm := ev.ChannelType == "im"
	var agents []string
	replyRef := ref(ev.Channel, "")
	if threaded {
		agents = e.routed(org, ev.Channel, ev.ThreadTS)
		replyRef = ref(ev.Channel, ev.ThreadTS)
	}
	if len(agents) == 0 && dm {
		agents = e.routed(org, ev.Channel, "")
	}
	if len(agents) == 0 && !dm {
		return // a channel message no agent is waiting on: pull only
	}
	notice := func(text string) {
		args := map[string]any{"channel": ev.Channel, "text": text}
		if threaded {
			args["thread_ts"] = ev.ThreadTS
		}
		_, _ = cl.Call(ctx, "chat.postMessage", args)
	}
	member, ok := e.Member(org, ev.User)
	if !ok {
		if dm {
			notice("You aren't linked to this Context Drop org, so agents won't see your messages. Run `context-drop slack connect` to link yourself, or ask someone in the org to run `context-drop slack allow @you`.")
		}
		return
	}
	if len(agents) == 0 {
		notice("No agent is waiting on you here. Agents reach you first; reply in the thread or DM they open.")
		return
	}
	where := "a DM"
	if !dm {
		where = "a thread in <#" + ev.Channel + ">"
	}
	text := fmt.Sprintf("Slack message from %s (a person on the org's Slack) in %s:\n\n%s\n\nReply to them with: context-drop send %s \"...\"",
		member.Name, where, ev.Text, replyRef)
	var failed []string
	for _, id := range agents {
		if perr := e.host.Deliver(ctx, org, id, text, member.Name+" on Slack"); perr != nil {
			e.host.Log().Info("slack delivery failed", "org", org, "agent", id, "err", perr.Message)
			failed = append(failed, fmt.Sprintf("`%s` (%s)", id, perr.Message))
		}
	}
	if len(failed) > 0 {
		notice("Not delivered to " + strings.Join(failed, ", ") + ".")
	}
	if len(failed) < len(agents) {
		_, _ = cl.Call(ctx, "reactions.add", map[string]any{"channel": ev.Channel, "timestamp": ev.TS, "name": "eyes"})
	}
}
