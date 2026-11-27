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
	"net/url"
	"slices"
	"strings"
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

// Ext connects orgs to Linear. With a Linear OAuth app (ClientID set), an admin approves the app in a
// browser and its comments appear as the app; without one, an org gives it an admin's API key.
type Ext struct {
	// The Linear OAuth app, if the server has one. WebhookSecret signs the app's webhooks.
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	// API is the GraphQL endpoint and TokenURL the OAuth token endpoint; empty means Linear's.
	API      string
	TokenURL string

	host extensions.Host
}

func (e *Ext) oauth() bool { return e.ClientID != "" }

// Install is an org's connection to a Linear workspace. Its key and webhook secret are sealed.
type Install struct {
	Org       string `json:"org"`
	LinearOrg string `json:"linear_org"`
	Name      string `json:"name"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	// An API key install: the key, and the webhook it made with its own secret.
	KeySealed    string `json:"key_sealed,omitempty"`
	WebhookID    string `json:"webhook_id,omitempty"`
	SecretSealed string `json:"secret_sealed,omitempty"`
	// An OAuth install: the app's tokens. Its webhooks are the app's, signed with Ext.WebhookSecret.
	TokenSealed   string    `json:"token_sealed,omitempty"`
	RefreshSealed string    `json:"refresh_sealed,omitempty"`
	Expires       time.Time `json:"expires,omitempty"`
	InstalledBy   string    `json:"installed_by"`
	InstalledAt   time.Time `json:"installed_at"`
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
	err := h.Each(Name, func(org string, data json.RawMessage) error {
		var st state
		if err := json.Unmarshal(data, &st); err != nil {
			return err
		}
		for _, i := range st.Installs {
			if err := h.Claim(Name, i.LinearOrg, org); err != nil {
				h.Log().Warn("linear workspace held by two orgs", "workspace", i.LinearOrg, "org", org, "err", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	mux.HandleFunc("POST /v1/linear/events", e.handleEvents)
	mux.HandleFunc("GET /v1/linear/oauth", e.handleOAuth)
	mux.HandleFunc("POST /v1/{org}/linear/install", h.Authed(e.handleInstall))
	mux.HandleFunc("POST /v1/{org}/linear/uninstall", h.Authed(e.handleUninstall))
	mux.HandleFunc("POST /v1/{org}/linear/status", h.Authed(e.handleStatus))
	mux.HandleFunc("POST /v1/{org}/linear/read", h.Authed(e.handleRead))
	return nil
}

func (e *Ext) get(org string) (state, error) {
	var st state
	_, err := e.host.Get(Name, org, &st)
	return st, err
}

// update changes an org's state, with no other server changing it in between.
func (e *Ext) update(ctx context.Context, org string, fn func(*state) error) error {
	var st state
	return e.host.Update(ctx, Name, org, &st, func() error { return fn(&st) })
}

func (e *Ext) install(org string) (Install, bool) {
	st, err := e.get(org)
	if err != nil || len(st.Installs) == 0 {
		return Install{}, false
	}
	return st.Installs[0], true
}

// client is the org's Linear client, refreshing an OAuth token that is about to expire.
func (e *Ext) client(ctx context.Context, org string) (Client, Install, *pool.Error) {
	in, ok := e.install(org)
	if !ok {
		return Client{}, in, pool.Errorf("not_found", "%v", ErrNotInstalled)
	}
	if in.KeySealed != "" {
		key, err := e.host.Unseal(in.KeySealed)
		if err != nil {
			return Client{}, in, pool.Errorf("internal", "%v", err)
		}
		return Client{Auth: key, URL: e.API}, in, nil
	}
	tok, err := e.token(ctx, org)
	if err != nil {
		return Client{}, in, pool.Errorf("forbidden", "Linear sign-in expired; run `context-drop linear install` again: %v", err)
	}
	return Client{Auth: "Bearer " + tok, URL: e.API}, in, nil
}

// token is an OAuth install's access token, refreshed when it has under five minutes left. The
// refresh holds the org's state, so two servers never spend the same refresh token.
func (e *Ext) token(ctx context.Context, org string) (string, error) {
	in, ok := e.install(org)
	if !ok {
		return "", ErrNotInstalled
	}
	if time.Until(in.Expires) > 5*time.Minute {
		return e.host.Unseal(in.TokenSealed)
	}
	var access string
	err := e.update(ctx, org, func(st *state) error {
		if len(st.Installs) == 0 {
			return ErrNotInstalled
		}
		cur := &st.Installs[0]
		if time.Until(cur.Expires) > 5*time.Minute { // another server refreshed it meanwhile
			var err error
			access, err = e.host.Unseal(cur.TokenSealed)
			return err
		}
		refresh, err := e.host.Unseal(cur.RefreshSealed)
		if err != nil {
			return err
		}
		tok, err := Exchange(ctx, e.TokenURL, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh},
			"client_id": {e.ClientID}, "client_secret": {e.ClientSecret}})
		if err != nil {
			return err
		}
		access = tok.Access
		return e.storeToken(cur, tok)
	})
	return access, err
}

// storeToken seals a token into an install.
func (e *Ext) storeToken(in *Install, tok Token) error {
	a, err := e.host.Seal(tok.Access)
	if err != nil {
		return err
	}
	r, err := e.host.Seal(tok.Refresh)
	if err != nil {
		return err
	}
	in.TokenSealed, in.RefreshSealed, in.Expires = a, r, tok.Expires
	return nil
}

// saveInstall records an install unless another org already has that Linear workspace.
func (e *Ext) saveInstall(inst Install) error {
	_, held, _ := e.host.Owner(Name, inst.LinearOrg)
	if err := e.host.Claim(Name, inst.LinearOrg, inst.Org); errors.Is(err, extensions.ErrTaken) {
		return errTaken
	} else if err != nil {
		return err
	}
	var old string
	err := e.update(context.Background(), inst.Org, func(st *state) error {
		if len(st.Installs) > 0 {
			old = st.Installs[0].LinearOrg
		}
		st.Installs = []Install{inst}
		return nil
	})
	if err != nil && !held {
		_ = e.host.Release(Name, inst.LinearOrg, inst.Org) // nothing was installed; free the workspace
	}
	if err == nil && old != "" && old != inst.LinearOrg {
		_ = e.host.Release(Name, old, inst.Org)
	}
	return err
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
	if in.Key == "" && e.oauth() {
		state := e.state(c.Org, c.Machine, time.Now().Add(30*time.Minute))
		extensions.WriteJSON(w, http.StatusOK, map[string]string{"url": AuthorizeURL(e.ClientID, e.redirect(), state)})
		return
	}
	if in.Key == "" {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", "give a Linear API key (this server has no Linear app)")
		return
	}
	cl := Client{Auth: in.Key, URL: e.API}
	who, err := cl.Whoami(r.Context())
	if err != nil {
		extensions.WriteError(w, http.StatusBadRequest, "bad_request", "Linear refused the key: "+err.Error())
		return
	}
	if owner, ok, _ := e.host.Owner(Name, who.OrgID); ok && owner != c.Org {
		extensions.WriteError(w, http.StatusConflict, "exists", "Linear workspace "+who.OrgName+" is already connected to another org")
		return
	}
	old, hadOld := e.install(c.Org)

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
			// saveInstall checks again: another org may have connected this workspace meanwhile.
			err = e.saveInstall(Install{Org: c.Org, LinearOrg: who.OrgID, Name: who.OrgName, UserID: who.UserID, UserName: who.UserName,
				KeySealed: keySealed, WebhookID: hook, SecretSealed: secretSealed, InstalledBy: c.Machine, InstalledAt: time.Now().UTC()})
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

// deleteWebhook removes an API key install's webhook with its own key; it is best effort. An OAuth
// install has no webhook of its own.
func (e *Ext) deleteWebhook(ctx context.Context, in Install) {
	if in.KeySealed == "" || in.WebhookID == "" {
		return
	}
	key, err := e.host.Unseal(in.KeySealed)
	if err != nil {
		return
	}
	if err := (Client{Auth: key, URL: e.API}).DeleteWebhook(ctx, in.WebhookID); err != nil {
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
	err := e.update(r.Context(), c.Org, func(st *state) error { *st = state{}; return nil })
	if ok && err == nil {
		_ = e.host.Release(Name, in.LinearOrg, c.Org)
	}
	if err != nil {
		extensions.WriteError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	extensions.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Status is what `linear status` returns.
type Status struct {
	Installed bool      `json:"installed"`
	Workspace string    `json:"workspace,omitempty"`
	User      string    `json:"user,omitempty"`
	Since     time.Time `json:"since,omitempty"`
}

func (e *Ext) handleStatus(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	in, ok := e.install(c.Org)
	extensions.WriteJSON(w, http.StatusOK, Status{Installed: ok, Workspace: in.Name, User: in.UserName, Since: in.InstalledAt})
}

// ── send and read ───────────────────────────────────────────────────────────

// Send comments on an issue. When an agent sends, later comments on the issue are pushed to it.
func (e *Ext) Send(ctx context.Context, c extensions.Caller, target, text, from, replyTo string) (pool.SendResult, *pool.Error) {
	cl, _, perr := e.client(ctx, c.Org)
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
	return e.update(context.Background(), org, func(st *state) error {
		now := time.Now().UTC()
		st.Routes = slices.DeleteFunc(st.Routes, func(r Route) bool { return now.Sub(r.At) > routeTTL })
		for i := range st.Routes {
			r := &st.Routes[i]
			if r.IssueID == issue.ID {
				r.At = now
				if !slices.Contains(r.Agents, agent) {
					r.Agents = append(r.Agents, agent)
				}
				return nil
			}
		}
		st.Routes = append(st.Routes, Route{Org: org, IssueID: issue.ID, Identifier: issue.Identifier, Agents: []string{agent}, At: now})
		return nil
	})
}

func (e *Ext) routed(org, issueID string) (Route, bool) {
	st, _ := e.get(org)
	for _, r := range st.Routes {
		if r.IssueID == issueID && time.Since(r.At) <= routeTTL {
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
	cl, _, perr := e.client(r.Context(), c.Org)
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

// markSeen records a comment as handled, by any server; it reports whether it was already.
func (e *Ext) markSeen(id string) bool { return !e.host.Once(Name, id) }

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
	if org, ok, _ := e.host.Owner(Name, hook.OrganizationID); ok {
		if in, ok := e.install(org); ok && in.LinearOrg == hook.OrganizationID {
			inst = in
		}
	}
	secret, err := e.WebhookSecret, error(nil)
	if inst.SecretSealed != "" {
		secret, err = e.host.Unseal(inst.SecretSealed)
	}
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
