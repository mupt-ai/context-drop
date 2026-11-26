package linear

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// Name is the extension's name and the capability a mod needs to use it.
const Name = "linear"

// Prefix starts every Linear send target: "linear:ENG-123".
const Prefix = "linear:"

// footer ends every comment an agent posts, so its own comments are never pushed back to it.
const footer = "reply in a comment to answer"

// Ext connects orgs to Linear. Each org gives it one API key; there is no app to create.
type Ext struct {
	// API is the GraphQL endpoint; empty means Linear's.
	API string

	host extensions.Host
	mu   sync.Mutex
	st   state
	seen map[string]time.Time // comment IDs already delivered, since Linear retries
}

// Install is an org's connection to a Linear workspace. Its key and webhook secret are sealed.
type Install struct {
	Org          string    `json:"org"`
	LinearOrg    string    `json:"linear_org"`
	Name         string    `json:"name"`
	UserID       string    `json:"user_id"`
	UserName     string    `json:"user_name"`
	KeySealed    string    `json:"key_sealed"`
	WebhookID    string    `json:"webhook_id"`
	SecretSealed string    `json:"secret_sealed"`
	InstalledBy  string    `json:"installed_by"`
	InstalledAt  time.Time `json:"installed_at"`
}

// Route sends new comments on an issue to the agents that commented on it.
type Route struct {
	Org        string    `json:"org"`
	IssueID    string    `json:"issue_id"`
	Identifier string    `json:"identifier"`
	Agents     []string  `json:"agents"`
	At         time.Time `json:"at"`
}

type state struct {
	Installs []Install `json:"installs,omitempty"`
	Routes   []Route   `json:"routes,omitempty"`
}

// routeTTL is how long a route lives after its last use.
const routeTTL = 30 * 24 * time.Hour

// errTaken means another org connected the same Linear workspace first.
var errTaken = errors.New("workspace taken")

// ErrNotInstalled means the org has not connected Linear.
var ErrNotInstalled = errors.New("this org has not connected Linear; run `context-drop linear install`")

// Name implements extensions.Extension.
func (e *Ext) Name() string { return Name }

// Owns reports whether a target is a Linear issue.
func (e *Ext) Owns(target string) bool { return strings.HasPrefix(target, Prefix) }

// Register implements extensions.Extension.
func (e *Ext) Register(mux *http.ServeMux, h extensions.Host) error {
	e.host = h
	if err := h.Load(Name, &e.st); err != nil {
		return err
	}
	mux.HandleFunc("POST /v1/linear/events", e.handleEvents)
	mux.HandleFunc("POST /v1/{org}/linear/install", h.Authed(e.handleInstall))
	mux.HandleFunc("POST /v1/{org}/linear/uninstall", h.Authed(e.handleUninstall))
	mux.HandleFunc("POST /v1/{org}/linear/status", h.Authed(e.handleStatus))
	mux.HandleFunc("POST /v1/{org}/linear/read", h.Authed(e.handleRead))
	return nil
}

func (e *Ext) save() error { return e.host.Save(Name, e.st) }

func (e *Ext) install(org string) (Install, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, in := range e.st.Installs {
		if in.Org == org {
			return in, true
		}
	}
	return Install{}, false
}

// client is the org's Linear client.
func (e *Ext) client(org string) (Client, Install, *pool.Error) {
	in, ok := e.install(org)
	if !ok {
		return Client{}, in, pool.Errorf("not_found", "%v", ErrNotInstalled)
	}
	key, err := e.host.Unseal(in.KeySealed)
	if err != nil {
		return Client{}, in, pool.Errorf("internal", "%v", err)
	}
	return Client{Key: key, URL: e.API}, in, nil
}

func asPoolError(err error) *pool.Error {
	var le *Error
	if errors.As(err, &le) {
		if strings.Contains(strings.ToLower(le.Message), "not found") || strings.HasPrefix(le.Message, "no issue") {
			return pool.Errorf("not_found", "%v", err)
		}
		return pool.Errorf("bad_request", "%v", err)
	}
	return pool.Errorf("internal", "%v", err)
}

// ── install ─────────────────────────────────────────────────────────────────

func (e *Ext) handleInstall(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if !extensions.Decode(w, r, &in) {
		return
	}
	in.Key = strings.TrimSpace(in.Key)
	if in.Key == "" {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", "give a Linear API key")
		return
	}
	cl := Client{Key: in.Key, URL: e.API}
	who, err := cl.Whoami(r.Context())
	if err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", "Linear refused the key: "+err.Error())
		return
	}
	e.mu.Lock()
	for _, i := range e.st.Installs {
		if i.LinearOrg == who.OrgID && i.Org != c.Org {
			e.mu.Unlock()
			extensions.WriteError(w, http.StatusConflict, "exists", "Linear workspace "+who.OrgName+" is already connected to another org")
			return
		}
	}
	old, hadOld := Install{}, false
	for _, i := range e.st.Installs {
		if i.Org == c.Org {
			old, hadOld = i, true
		}
	}
	e.mu.Unlock()

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	secret := hex.EncodeToString(buf)
	hook, err := cl.CreateWebhook(r.Context(), strings.TrimRight(e.host.PublicURL(), "/")+"/v1/linear/events", secret)
	if err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", "Linear did not create the webhook (the key must belong to a workspace admin): "+err.Error())
		return
	}
	keySealed, err := e.host.Seal(in.Key)
	if err == nil {
		var secretSealed string
		if secretSealed, err = e.host.Seal(secret); err == nil {
			inst := Install{Org: c.Org, LinearOrg: who.OrgID, Name: who.OrgName, UserID: who.UserID, UserName: who.UserName,
				KeySealed: keySealed, WebhookID: hook, SecretSealed: secretSealed, InstalledBy: c.Machine, InstalledAt: time.Now().UTC()}
			e.mu.Lock()
			// Check again: another org may have connected this workspace while the webhook was made.
			if slices.ContainsFunc(e.st.Installs, func(i Install) bool { return i.LinearOrg == who.OrgID && i.Org != c.Org }) {
				err = errTaken
			} else {
				e.st.Installs = append(slices.DeleteFunc(e.st.Installs, func(i Install) bool { return i.Org == c.Org }), inst)
				err = e.save()
			}
			e.mu.Unlock()
		}
	}
	if err != nil {
		_ = cl.DeleteWebhook(r.Context(), hook)
		if errors.Is(err, errTaken) {
			extensions.WriteError(w, http.StatusConflict, "exists", "Linear workspace "+who.OrgName+" is already connected to another org")
			return
		}
		extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	if hadOld && old.WebhookID != "" {
		e.deleteWebhook(r.Context(), old)
	}
	e.host.Log().Info("linear connected", "org", c.Org, "workspace", who.OrgID, "by", c.Machine)
	extensions.WriteJSON(w, http.StatusOK, map[string]string{"workspace": who.OrgName, "user": who.UserName})
}

// deleteWebhook removes an install's webhook with its own key; it is best effort.
func (e *Ext) deleteWebhook(ctx context.Context, in Install) {
	key, err := e.host.Unseal(in.KeySealed)
	if err != nil {
		return
	}
	if err := (Client{Key: key, URL: e.API}).DeleteWebhook(ctx, in.WebhookID); err != nil {
		e.host.Log().Warn("linear webhook not deleted", "org", in.Org, "err", err)
	}
}

func (e *Ext) handleUninstall(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	in, ok := e.install(c.Org)
	if ok {
		e.deleteWebhook(r.Context(), in)
	}
	e.mu.Lock()
	e.st.Installs = slices.DeleteFunc(e.st.Installs, func(i Install) bool { return i.Org == c.Org })
	e.st.Routes = slices.DeleteFunc(e.st.Routes, func(rt Route) bool { return rt.Org == c.Org })
	err := e.save()
	e.mu.Unlock()
	if err != nil {
		extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	extensions.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Status is what `linear status` returns.
type Status struct {
	Installed bool   `json:"installed"`
	Workspace string `json:"workspace,omitempty"`
	User      string `json:"user,omitempty"`
}

func (e *Ext) handleStatus(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	in, ok := e.install(c.Org)
	extensions.WriteJSON(w, http.StatusOK, Status{Installed: ok, Workspace: in.Name, User: in.UserName})
}

// ── send and read ───────────────────────────────────────────────────────────

// Send comments on an issue. When an agent sends, later comments on the issue are pushed to it.
func (e *Ext) Send(ctx context.Context, c extensions.Caller, target, text, from, replyTo string) (pool.SendResult, *pool.Error) {
	cl, _, perr := e.client(c.Org)
	if perr != nil {
		return pool.SendResult{}, perr
	}
	issue, err := cl.Issue(ctx, strings.TrimPrefix(target, Prefix))
	if err != nil {
		return pool.SendResult{}, asPoolError(err)
	}
	if replyTo != "" {
		text += "\n\n_— agent `" + replyTo + "`; " + footer + "_"
	} else {
		text += "\n\n_— " + from + "_"
	}
	cm, err := cl.Comment(ctx, issue.ID, text)
	if err != nil {
		return pool.SendResult{}, asPoolError(err)
	}
	e.markSeen(cm.ID)
	if replyTo != "" {
		if err := e.route(c.Org, issue, replyTo); err != nil {
			e.host.Log().Warn("linear route", "err", err)
		}
	}
	return pool.SendResult{ID: Prefix + issue.Identifier, Delivered: true, Via: Name}, nil
}

func (e *Ext) route(org string, issue Issue, agent string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().UTC()
	e.st.Routes = slices.DeleteFunc(e.st.Routes, func(r Route) bool { return now.Sub(r.At) > routeTTL })
	for i := range e.st.Routes {
		r := &e.st.Routes[i]
		if r.Org == org && r.IssueID == issue.ID {
			r.At = now
			if !slices.Contains(r.Agents, agent) {
				r.Agents = append(r.Agents, agent)
			}
			return e.save()
		}
	}
	e.st.Routes = append(e.st.Routes, Route{Org: org, IssueID: issue.ID, Identifier: issue.Identifier, Agents: []string{agent}, At: now})
	return e.save()
}

func (e *Ext) routed(org, issueID string) (Route, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.st.Routes {
		if r.Org == org && r.IssueID == issueID && time.Since(r.At) <= routeTTL {
			r.Agents = slices.Clone(r.Agents)
			return r, true
		}
	}
	return Route{}, false
}

// ReadRequest reads an issue's comments.
type ReadRequest struct {
	Target string `json:"target"` // "linear:ENG-123"
	Limit  int    `json:"limit,omitempty"`
}

// ReadResult is an issue and its latest comments, oldest first.
type ReadResult struct {
	Issue    Issue     `json:"issue"`
	Comments []Comment `json:"comments"`
}

func (e *Ext) handleRead(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.Allowed(w, c, Name) {
		return
	}
	var in ReadRequest
	if !extensions.Decode(w, r, &in) {
		return
	}
	cl, _, perr := e.client(c.Org)
	if perr != nil {
		extensions.Fail(w, perr)
		return
	}
	issue, err := cl.Issue(r.Context(), strings.TrimPrefix(in.Target, Prefix))
	if err != nil {
		extensions.Fail(w, asPoolError(err))
		return
	}
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	list, err := cl.Comments(r.Context(), issue.ID, limit)
	if err != nil {
		extensions.Fail(w, asPoolError(err))
		return
	}
	extensions.WriteJSON(w, http.StatusOK, ReadResult{Issue: issue, Comments: list})
}

// ── webhooks: people answering agents ───────────────────────────────────────

// markSeen records a comment as handled; it reports whether it was already.
func (e *Ext) markSeen(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seen == nil {
		e.seen = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range e.seen {
		if now.Sub(t) > time.Hour {
			delete(e.seen, k)
		}
	}
	if _, ok := e.seen[id]; ok {
		return true
	}
	e.seen[id] = now
	return false
}

type webhook struct {
	Action         string `json:"action"`
	Type           string `json:"type"`
	OrganizationID string `json:"organizationId"`
	Actor          struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"actor"`
	Data struct {
		ID      string `json:"id"`
		Body    string `json:"body"`
		IssueID string `json:"issueId"`
	} `json:"data"`
}

func (e *Ext) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var hook webhook
	if err := json.Unmarshal(body, &hook); err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var inst Install
	e.mu.Lock()
	for _, i := range e.st.Installs {
		if i.LinearOrg == hook.OrganizationID {
			inst = i
		}
	}
	e.mu.Unlock()
	secret, err := e.host.Unseal(inst.SecretSealed)
	if inst.Org == "" || err != nil || Verify(secret, r.Header, body, time.Now()) != nil {
		extensions.WriteError(w, http.StatusUnauthorized, "unauthorized", "bad or unknown webhook")
		return
	}
	w.WriteHeader(http.StatusOK)
	if hook.Type != "Comment" || hook.Action != "create" || hook.Data.IssueID == "" || e.markSeen(hook.Data.ID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		e.deliver(ctx, inst.Org, hook)
	}()
}

// deliver pushes a person's comment to the agents that commented on the issue. Comments on other
// issues, and the agents' own comments, go nowhere.
func (e *Ext) deliver(ctx context.Context, org string, hook webhook) {
	if strings.Contains(hook.Data.Body, footer) {
		return // an agent's own comment
	}
	rt, ok := e.routed(org, hook.Data.IssueID)
	if !ok {
		return
	}
	who := hook.Actor.Name
	if who == "" {
		who = "someone"
	}
	text := fmt.Sprintf("Linear comment from %s (a person on the org's Linear) on %s:\n\n%s\n\nReply to them with: context-drop send %s%s \"...\"",
		who, rt.Identifier, hook.Data.Body, Prefix, rt.Identifier)
	for _, id := range rt.Agents {
		if perr := e.host.Deliver(ctx, org, id, text, who+" on Linear"); perr != nil {
			e.host.Log().Info("linear delivery failed", "org", org, "agent", id, "err", perr.Message)
		}
	}
}
