package orgserver

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

	"contextdrop.dev/context-drop/internal/pool"
	"contextdrop.dev/context-drop/internal/slack"
)

// SlackApp is the server's Slack app. Each org installs it into its own workspace.
type SlackApp struct {
	ClientID      string
	ClientSecret  string
	SigningSecret string
	// PublicURL is the server's public origin, for the OAuth redirect.
	PublicURL string
	// API is the Slack Web API base; empty means slack.com.
	API string
}

func (a *SlackApp) redirect() string { return strings.TrimRight(a.PublicURL, "/") + "/v1/slack/oauth" }

// slackState holds what Slack-side lookups the server has cached, and the events it has seen.
type slackState struct {
	mu     sync.Mutex
	names  map[string]string    // org|kind|key → value: channel names → IDs, user IDs → names
	events map[string]time.Time // event IDs already handled, since Slack retries
}

func (s *Server) slackCache(org, kind, key string) (string, bool) {
	s.slack.mu.Lock()
	defer s.slack.mu.Unlock()
	v, ok := s.slack.names[org+"|"+kind+"|"+key]
	return v, ok
}

func (s *Server) slackRemember(org, kind, key, value string) {
	s.slack.mu.Lock()
	defer s.slack.mu.Unlock()
	if s.slack.names == nil {
		s.slack.names = map[string]string{}
	}
	s.slack.names[org+"|"+kind+"|"+key] = value
}

// seenEvent reports whether an event was handled already, and marks it handled.
func (s *Server) seenEvent(id string) bool {
	if id == "" {
		return false
	}
	s.slack.mu.Lock()
	defer s.slack.mu.Unlock()
	if s.slack.events == nil {
		s.slack.events = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range s.slack.events {
		if now.Sub(t) > time.Hour {
			delete(s.slack.events, k)
		}
	}
	if _, ok := s.slack.events[id]; ok {
		return true
	}
	s.slack.events[id] = now
	return false
}

func (s *Server) registerSlack(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/slack/oauth", s.handleSlackOAuth)
	mux.HandleFunc("GET /v1/slack/link", s.handleSlackLink)
	mux.HandleFunc("POST /v1/{org}/slack/connect", s.authed(s.handleSlackConnect))
	mux.HandleFunc("POST /v1/slack/events", s.handleSlackEvents)
	mux.HandleFunc("POST /v1/{org}/slack/install", s.authed(s.handleSlackInstall))
	mux.HandleFunc("POST /v1/{org}/slack/uninstall", s.authed(s.handleSlackUninstall))
	mux.HandleFunc("POST /v1/{org}/slack/status", s.authed(s.handleSlackStatus))
	mux.HandleFunc("POST /v1/{org}/slack/members", s.authed(s.handleSlackMembers))
	mux.HandleFunc("POST /v1/{org}/slack/read", s.authed(s.handleSlackRead))
	mux.HandleFunc("POST /v1/{org}/slack/api", s.authed(s.handleSlackAPI))
}

func (s *Server) slackOn(w http.ResponseWriter) bool {
	if s.Slack == nil || s.Slack.ClientID == "" {
		writeError(w, http.StatusNotImplemented, "unsupported", "Slack is not configured on this server")
		return false
	}
	return true
}

// slackClient is the org's Web API client.
func (s *Server) slackClient(org string) (slack.Client, SlackInstall, *pool.Error) {
	if s.Slack == nil {
		return slack.Client{}, SlackInstall{}, pool.Errorf("unsupported", "Slack is not configured on this server")
	}
	in, tok, err := s.store.Slack(org)
	if errors.Is(err, ErrSlackNotInstalled) {
		return slack.Client{}, in, pool.Errorf("not_found", "%v", err)
	}
	if err != nil {
		return slack.Client{}, in, pool.Errorf("internal", "%v", err)
	}
	return slack.Client{Token: tok, Base: s.Slack.API}, in, nil
}

func slackErr(err error) *pool.Error {
	var se *slack.Error
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

// installState signs what an OAuth round trip is for ("install" a workspace, or "connect" a person), the
// org and machine that asked, and when the request expires.
func (s *Server) installState(kind, org, machine string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(kind + "\n" + org + "\n" + machine + "\n" + strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, []byte(s.Slack.ClientSecret))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) checkState(state string) (kind, org, machine string, ok bool) {
	payload, sig, found := strings.Cut(state, ".")
	if !found {
		return "", "", "", false
	}
	mac := hmac.New(sha256.New, []byte(s.Slack.ClientSecret))
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

func (s *Server) handleSlackInstall(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) || !s.slackOn(w) {
		return
	}
	state := s.installState("install", c.org, c.machine, time.Now().Add(30*time.Minute))
	writeJSON(w, http.StatusOK, map[string]string{"url": slack.AuthorizeURL(s.Slack.ClientID, s.Slack.redirect(), state)})
}

func (s *Server) handleSlackOAuth(w http.ResponseWriter, r *http.Request) {
	page := func(status int, title, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Context Drop</title><body style=\"font-family:system-ui;max-width:36rem;margin:4rem auto\"><h1>%s</h1><p>%s</p>",
			html.EscapeString(title), html.EscapeString(body))
	}
	if s.Slack == nil || s.Slack.ClientID == "" {
		page(http.StatusNotImplemented, "Slack is off", "Slack is not configured on this server.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		page(http.StatusBadRequest, "Not connected", "Slack said: "+e)
		return
	}
	kind, org, machine, ok := s.checkState(r.URL.Query().Get("state"))
	if !ok {
		page(http.StatusBadRequest, "Link expired", "Run `context-drop slack install` again for a fresh link.")
		return
	}
	if kind == "connect" {
		s.finishConnect(w, r, org, machine)
		return
	}
	got, err := slack.Exchange(r.Context(), s.Slack.API, s.Slack.ClientID, s.Slack.ClientSecret, r.URL.Query().Get("code"), s.Slack.redirect())
	if err != nil {
		page(http.StatusBadGateway, "Not connected", err.Error())
		return
	}
	in := SlackInstall{Org: org, TeamID: got.TeamID, TeamName: got.TeamName, BotUserID: got.BotUserID, InstalledBy: machine}
	if err := s.store.InstallSlack(in, got.Token); err != nil {
		page(http.StatusConflict, "Not connected", err.Error())
		return
	}
	s.log.Info("slack connected", "org", org, "team", got.TeamID, "by", machine)
	// Whoever approved the install is linked too, so they can answer agents right away.
	linked := ""
	if got.InstallerID != "" {
		name := s.userName(r.Context(), slack.Client{Token: got.Token, Base: s.Slack.API}, org, got.InstallerID)
		if err := s.store.AddSlackMember(SlackMember{Org: org, UserID: got.InstallerID, Name: name, AddedBy: machine}); err == nil {
			linked = " You (" + name + ") are linked, so agents can reach you."
		}
	}
	page(http.StatusOK, "Slack connected", fmt.Sprintf("Workspace %s is connected to org %s.%s Everyone else links themselves with `context-drop slack connect`.", got.TeamName, org, linked))
}

func (s *Server) handleSlackUninstall(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) {
		return
	}
	if err := s.store.UninstallSlack(c.org); err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SlackStatus is what `slack status` returns.
type SlackStatus struct {
	Configured bool          `json:"configured"`
	Installed  bool          `json:"installed"`
	Team       string        `json:"team,omitempty"`
	TeamID     string        `json:"team_id,omitempty"`
	BotUserID  string        `json:"bot_user_id,omitempty"`
	Members    []SlackMember `json:"members"`
}

func (s *Server) handleSlackStatus(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) {
		return
	}
	out := SlackStatus{Configured: s.Slack != nil && s.Slack.ClientID != "", Members: s.store.SlackMembers(c.org)}
	if out.Members == nil {
		out.Members = []SlackMember{}
	}
	if in, _, err := s.store.Slack(c.org); err == nil {
		out.Installed, out.Team, out.TeamID, out.BotUserID = true, in.TeamName, in.TeamID, in.BotUserID
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSlackMembers links and unlinks the Slack users whose DMs and thread replies reach agents.
// Only machines may change it: who can prompt the org's agents is not a mod's call.
func (s *Server) handleSlackMembers(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) {
		return
	}
	var in struct {
		Op   string `json:"op"` // add, remove, list
		User string `json:"user"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Op == "" || in.Op == "list" {
		writeJSON(w, http.StatusOK, s.store.SlackMembers(c.org))
		return
	}
	cl, _, perr := s.slackClient(c.org)
	if perr != nil {
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	id, name, err := s.findUser(r.Context(), cl, c.org, in.User)
	if err != nil {
		perr := slackErr(err)
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	switch in.Op {
	case "add":
		m := SlackMember{Org: c.org, UserID: id, Name: name, AddedBy: c.machine}
		if err := s.store.AddSlackMember(m); err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, m)
	case "remove":
		ok, err := s.store.RemoveSlackMember(c.org, id)
		if err != nil {
			storeError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", name+" is not linked to this org")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"user_id": id, "name": name})
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "op is add, remove, or list")
	}
}

// ── finding people and channels ─────────────────────────────────────────────

var (
	slackUserID    = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)
	slackChannelID = regexp.MustCompile(`^[CGD][A-Z0-9]{6,}$`)
)

// findUser resolves "@handle", "@email", or a user ID to a user ID and display name.
func (s *Server) findUser(ctx context.Context, cl slack.Client, org, who string) (id, name string, err error) {
	who = strings.TrimPrefix(strings.TrimSpace(who), "@")
	if who == "" {
		return "", "", pool.Errorf("bad_request", "name a Slack user: @handle, @email, or a user ID")
	}
	if slackUserID.MatchString(who) {
		return who, s.userName(ctx, cl, org, who), nil
	}
	if strings.Contains(who, "@") {
		raw, err := cl.Get(ctx, "users.lookupByEmail", url.Values{"email": {who}})
		if err != nil {
			return "", "", err
		}
		var out struct{ User slackUser }
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", "", err
		}
		return out.User.ID, out.User.display(), nil
	}
	if id, ok := s.slackCache(org, "handle", strings.ToLower(who)); ok {
		return id, s.userName(ctx, cl, org, id), nil
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
			Members  []slackUser `json:"members"`
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
					s.slackRemember(org, "handle", strings.ToLower(who), u.ID)
					s.slackRemember(org, "user", u.ID, u.display())
					return u.ID, u.display(), nil
				}
			}
		}
		if cursor = page.Metadata.NextCursor; cursor == "" {
			return "", "", pool.Errorf("not_found", "no Slack user named %s", who)
		}
	}
}

type slackUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
	Profile struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

func (u slackUser) display() string {
	for _, n := range []string{u.Profile.DisplayName, u.Profile.RealName, u.Name} {
		if n != "" {
			return n
		}
	}
	return u.ID
}

// userName is a user's display name, or their ID when it cannot be looked up.
func (s *Server) userName(ctx context.Context, cl slack.Client, org, id string) string {
	if n, ok := s.slackCache(org, "user", id); ok {
		return n
	}
	raw, err := cl.Get(ctx, "users.info", url.Values{"user": {id}})
	if err != nil {
		return id
	}
	var out struct{ User slackUser }
	if json.Unmarshal(raw, &out) != nil {
		return id
	}
	s.slackRemember(org, "user", id, out.User.display())
	return out.User.display()
}

// findChannel resolves "#name" or a channel ID to a channel ID.
func (s *Server) findChannel(ctx context.Context, cl slack.Client, org, name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "#")
	if slackChannelID.MatchString(name) {
		return name, nil
	}
	if id, ok := s.slackCache(org, "channel", name); ok {
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
			s.slackRemember(org, "channel", ch.Name, ch.ID)
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
func (s *Server) resolve(ctx context.Context, cl slack.Client, org, target string) (conversation, error) {
	target = strings.TrimSpace(target)
	switch {
	case strings.HasPrefix(target, "slack:"):
		ch, ts, _ := strings.Cut(strings.TrimPrefix(target, "slack:"), "/")
		if !slackChannelID.MatchString(ch) {
			return conversation{}, pool.Errorf("bad_request", "%q is not a Slack thread like slack:C0123ABCD/1700000000.000100", target)
		}
		return conversation{channel: ch, thread: ts, dm: strings.HasPrefix(ch, "D")}, nil
	case strings.HasPrefix(target, "@"):
		id, _, err := s.findUser(ctx, cl, org, target)
		if err != nil {
			return conversation{}, err
		}
		if ch, ok := s.slackCache(org, "dm", id); ok {
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
		s.slackRemember(org, "dm", id, out.Channel.ID)
		return conversation{channel: out.Channel.ID, dm: true}, nil
	default:
		ch, err := s.findChannel(ctx, cl, org, target)
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

// slackSend posts a message for people. When an agent sends, replies in the thread it starts (or
// continues), and in a DM it opens, are routed back to that agent.
func (s *Server) slackSend(w http.ResponseWriter, r *http.Request, c caller, in pool.SendRequest, from, replyTo string) {
	cl, _, perr := s.slackClient(c.org)
	if perr != nil {
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	conv, err := s.resolve(r.Context(), cl, c.org, in.Slack)
	if err != nil {
		perr := slackErr(err)
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	who := from
	if replyTo != "" {
		who = "agent `" + replyTo + "`"
	}
	text := in.Text + "\n\n_— " + who + "; reply in this thread to answer_"
	if replyTo == "" {
		text = in.Text + "\n\n_— " + who + "_"
	}
	args := map[string]any{"channel": conv.channel, "text": text, "unfurl_links": false, "unfurl_media": false}
	if conv.thread != "" {
		args["thread_ts"] = conv.thread
	}
	raw, err := cl.Call(r.Context(), "chat.postMessage", args)
	var se *slack.Error
	if errors.As(err, &se) && se.Code == "not_in_channel" && strings.HasPrefix(conv.channel, "C") {
		// A public channel the app is not in yet: join it and try once more.
		if _, jerr := cl.Call(r.Context(), "conversations.join", map[string]any{"channel": conv.channel}); jerr == nil {
			raw, err = cl.Call(r.Context(), "chat.postMessage", args)
		}
	}
	if err != nil {
		perr := slackErr(err)
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	var posted struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := json.Unmarshal(raw, &posted); err != nil {
		writeError(w, http.StatusBadGateway, "internal", err.Error())
		return
	}
	root := conv.thread
	if root == "" {
		root = posted.TS
	}
	if replyTo != "" {
		if err := s.store.RouteSlack(c.org, posted.Channel, root, replyTo); err != nil {
			s.log.Warn("slack route", "err", err)
		}
		if conv.dm {
			if err := s.store.RouteSlack(c.org, posted.Channel, "", replyTo); err != nil {
				s.log.Warn("slack route", "err", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, pool.SendResponse{Results: []pool.SendResult{{ID: ref(posted.Channel, root), Delivered: true, Via: "slack", TS: posted.TS}}})
}

type slackMsg struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
}

func (s *Server) handleSlackRead(w http.ResponseWriter, r *http.Request, c caller) {
	if !allowed(w, c, "slack") {
		return
	}
	var in pool.SlackReadRequest
	if !decode(w, r, &in) {
		return
	}
	cl, inst, perr := s.slackClient(c.org)
	if perr != nil {
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	out, err := s.slackRead(r.Context(), cl, inst, c.org, in)
	if err != nil {
		perr := slackErr(err)
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) slackRead(ctx context.Context, cl slack.Client, inst SlackInstall, org string, in pool.SlackReadRequest) (pool.SlackReadResult, error) {
	conv, err := s.resolve(ctx, cl, org, in.Target)
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
		Messages []slackMsg `json:"messages"`
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
			sm.Name = s.userName(ctx, cl, org, m.User)
		}
		msgs = append(msgs, sm)
	}
	sort.Slice(msgs, func(i, j int) bool { return slackTS(msgs[i].TS) < slackTS(msgs[j].TS) })
	return pool.SlackReadResult{Channel: conv.channel, Messages: msgs}, nil
}

func slackTS(ts string) float64 {
	f, _ := strconv.ParseFloat(ts, 64)
	return f
}

// slackMethods are the Web API method families a mod may call. Anything that administers the
// workspace or the app's own install is left out.
var slackMethods = []string{"chat.", "conversations.", "reactions.", "pins.", "bookmarks.", "users.info", "users.list", "users.lookupByEmail", "users.profile.get", "team.info", "files.info", "files.list"}

func slackMethodAllowed(m string) bool {
	if m == "chat.delete" || strings.HasPrefix(m, "conversations.kick") || m == "conversations.archive" || m == "conversations.rename" {
		return false
	}
	for _, p := range slackMethods {
		if strings.HasSuffix(p, ".") && strings.HasPrefix(m, p) || m == p {
			return true
		}
	}
	return false
}

// handleSlackAPI calls one Slack Web API method with the org's bot token, for mods and tools.
func (s *Server) handleSlackAPI(w http.ResponseWriter, r *http.Request, c caller) {
	if !allowed(w, c, "slack") {
		return
	}
	var in struct {
		Method string         `json:"method"`
		Args   map[string]any `json:"args"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slackMethodAllowed(in.Method) {
		writeError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("Slack method %q is not available (allowed: %s)", in.Method, strings.Join(slackMethods, ", ")))
		return
	}
	cl, _, perr := s.slackClient(c.org)
	if perr != nil {
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	if in.Args == nil {
		in.Args = map[string]any{}
	}
	raw, err := cl.Call(r.Context(), in.Method, in.Args)
	if err != nil {
		perr := slackErr(err)
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// ── events: people answering agents ─────────────────────────────────────────

type slackEvent struct {
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

func (s *Server) handleSlackEvents(w http.ResponseWriter, r *http.Request) {
	if s.Slack == nil || s.Slack.SigningSecret == "" {
		writeError(w, http.StatusNotImplemented, "unsupported", "Slack is not configured on this server")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := slack.Verify(s.Slack.SigningSecret, r.Header, body, time.Now()); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	var env struct {
		Type      string     `json:"type"`
		Challenge string     `json:"challenge"`
		TeamID    string     `json:"team_id"`
		EventID   string     `json:"event_id"`
		Event     slackEvent `json:"event"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if env.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, env.Challenge)
		return
	}
	// Slack wants an answer within three seconds, and retries otherwise; deliver in the background.
	w.WriteHeader(http.StatusOK)
	if env.Type != "event_callback" || s.seenEvent(env.EventID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.slackDeliver(ctx, env.TeamID, env.Event)
	}()
}

// slackDeliver pushes a person's message to the agents waiting on it: a reply in a thread an agent
// posted in goes to every agent in that thread, and a DM goes to the agent that last opened the DM.
// Anything else in a channel stays there, for agents to pull with a read.
func (s *Server) slackDeliver(ctx context.Context, team string, ev slackEvent) {
	if ev.Type != "message" || ev.BotID != "" || ev.User == "" {
		return
	}
	if ev.Subtype != "" && ev.Subtype != "file_share" && ev.Subtype != "thread_broadcast" {
		return
	}
	inst, tok, err := s.store.SlackByTeam(team)
	if err != nil || ev.User == inst.BotUserID {
		return
	}
	org := inst.Org
	cl := slack.Client{Token: tok, Base: s.Slack.API}
	threaded := ev.ThreadTS != "" && ev.ThreadTS != ev.TS
	dm := ev.ChannelType == "im"
	var agents []string
	replyRef := ref(ev.Channel, "")
	if threaded {
		agents = s.store.SlackRouted(org, ev.Channel, ev.ThreadTS)
		replyRef = ref(ev.Channel, ev.ThreadTS)
	}
	if len(agents) == 0 && dm {
		agents = s.store.SlackRouted(org, ev.Channel, "")
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
	member, ok := s.store.SlackMember(org, ev.User)
	if !ok {
		if dm {
			notice("You aren't linked to this Context Drop org, so agents won't see your messages. Someone in the org can add you with `context-drop slack allow @you`.")
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
		machine, session, ok := pool.SplitID(id)
		if !ok {
			continue
		}
		if _, perr := s.call(ctx, org, machine, "send", pool.LocalSend{ID: session, Text: text, From: member.Name + " on Slack"}); perr != nil {
			s.log.Info("slack delivery failed", "org", org, "agent", id, "err", perr.Message)
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
