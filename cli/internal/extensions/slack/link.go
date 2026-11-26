package slack

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/extensions"
)

// connectTTL is how long a person has to finish signing in after a machine starts a link.
const connectTTL = 10 * time.Minute

// handleConnect starts a person's sign-in: a machine asks for a link for its own person. The link is
// for whoever is at that machine, so the person signs in as themselves, not as someone else.
func (e *Ext) handleConnect(w http.ResponseWriter, r *http.Request, c extensions.Caller) {
	if !extensions.MachinesOnly(w, c) {
		return
	}
	if _, _, err := e.workspace(c.Org); err != nil {
		extensions.WriteError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	state := e.state("connect", c.Org, c.Machine, time.Now().Add(connectTTL))
	base := strings.TrimRight(e.host.PublicURL(), "/")
	extensions.WriteJSON(w, http.StatusOK, map[string]string{"url": base + "/v1/slack/link?state=" + url.QueryEscape(state)})
}

// handleLink shows who is asking before it sends the person to Slack. Without this step, a link sent
// to someone else would link them to an org they never chose.
func (e *Ext) handleLink(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	kind, org, machine, ok := e.checkState(state)
	if !ok || kind != "connect" {
		extensions.Page(w, http.StatusBadRequest, "Link expired", "<p>Run <code>context-drop slack connect</code> again for a fresh link.</p>")
		return
	}
	signIn := UserAuthorizeURL(e.ClientID, e.redirect(), state)
	extensions.Page(w, http.StatusOK, "Link your Slack account",
		fmt.Sprintf("<p>Sign in with Slack so the agents in <b>%s</b> can reach you on Slack and you can reply to them.</p>"+
			"<p>This was started from the machine <b>%s</b>. Continue only if you started it.</p>"+
			"<p><a href=\"%s\" style=\"display:inline-block;padding:.6rem 1rem;background:#4a154b;color:#fff;border-radius:6px;text-decoration:none\">Sign in with Slack</a></p>",
			html.EscapeString(org), html.EscapeString(machine), html.EscapeString(signIn)))
}

// finishConnect runs when Slack sends a signed-in person back: it links that person to the org.
func (e *Ext) finishConnect(w http.ResponseWriter, r *http.Request, org, machine string) {
	inst, _, err := e.workspace(org)
	if err != nil {
		extensions.Page(w, http.StatusNotFound, "Not linked", "<p>This org is no longer connected to Slack.</p>")
		return
	}
	person, err := ExchangeUser(r.Context(), e.API, e.ClientID, e.ClientSecret, r.URL.Query().Get("code"), e.redirect())
	if err != nil {
		extensions.Page(w, http.StatusBadGateway, "Not linked", "<p>"+html.EscapeString(err.Error())+"</p>")
		return
	}
	if person.TeamID != inst.TeamID {
		extensions.Page(w, http.StatusForbidden, "Not linked", "<p>That Slack account is not in this org's workspace.</p>")
		return
	}
	cl, _, perr := e.client(org)
	if perr != nil {
		extensions.Page(w, http.StatusBadGateway, "Not linked", "<p>"+html.EscapeString(perr.Message)+"</p>")
		return
	}
	name := person.Name
	if name == "" {
		name = e.userName(r.Context(), cl, org, person.ID)
	}
	if err := e.addMember(Member{Org: org, UserID: person.ID, Name: name, AddedBy: machine}); err != nil {
		extensions.Page(w, http.StatusInternalServerError, "Not linked", "<p>"+html.EscapeString(err.Error())+"</p>")
		return
	}
	e.host.Log().Info("slack member linked", "org", org, "user", person.ID, "by", machine)
	extensions.Page(w, http.StatusOK, "You're connected",
		fmt.Sprintf("<p><b>%s</b> is linked to <b>%s</b>. Agents there can now reach you on Slack. You can also DM the app to message them.</p>"+
			"<p>You can close this tab.</p>", html.EscapeString(name), html.EscapeString(org)))
}
