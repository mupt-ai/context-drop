package orgserver

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/slack"
)

// connectTTL is how long a person has to finish signing in after a machine starts a link.
const connectTTL = 10 * time.Minute

// slackPage writes a small page. body is HTML; callers escape anything they put in it.
func slackPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\"><title>Context Drop</title>"+
		"<body style=\"font-family:system-ui;max-width:36rem;margin:4rem auto;padding:0 1rem\"><h1>%s</h1>%s", html.EscapeString(title), body)
}

// handleSlackConnect starts a person's sign-in: a machine asks for a link for its own person. The link
// is for whoever is at that machine, so the person signs in as themselves, not as someone else.
func (s *Server) handleSlackConnect(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) || !s.slackOn(w) {
		return
	}
	if _, _, err := s.store.Slack(c.org); err != nil {
		storeError(w, err)
		return
	}
	state := s.installState("connect", c.org, c.machine, time.Now().Add(connectTTL))
	base := strings.TrimRight(s.Slack.PublicURL, "/")
	writeJSON(w, http.StatusOK, map[string]string{"url": base + "/v1/slack/link?state=" + url.QueryEscape(state)})
}

// handleSlackLink shows who is asking before it sends the person to Slack. Without this step, a link
// sent to someone else would link them to an org they never chose.
func (s *Server) handleSlackLink(w http.ResponseWriter, r *http.Request) {
	if s.Slack == nil || s.Slack.ClientID == "" {
		slackPage(w, http.StatusNotImplemented, "Slack is off", "<p>Slack is not configured on this server.</p>")
		return
	}
	state := r.URL.Query().Get("state")
	kind, org, machine, ok := s.checkState(state)
	if !ok || kind != "connect" {
		slackPage(w, http.StatusBadRequest, "Link expired", "<p>Run <code>context-drop slack connect</code> again for a fresh link.</p>")
		return
	}
	signIn := slack.UserAuthorizeURL(s.Slack.ClientID, s.Slack.redirect(), state)
	slackPage(w, http.StatusOK, "Link your Slack account",
		fmt.Sprintf("<p>Sign in with Slack so the agents in <b>%s</b> can reach you on Slack and you can reply to them.</p>"+
			"<p>This was started from the machine <b>%s</b>. Continue only if you started it.</p>"+
			"<p><a href=\"%s\" style=\"display:inline-block;padding:.6rem 1rem;background:#4a154b;color:#fff;border-radius:6px;text-decoration:none\">Sign in with Slack</a></p>",
			html.EscapeString(org), html.EscapeString(machine), html.EscapeString(signIn)))
}

// finishConnect runs when Slack sends a signed-in person back: it links that person to the org.
func (s *Server) finishConnect(w http.ResponseWriter, r *http.Request, org, machine string) {
	inst, _, err := s.store.Slack(org)
	if err != nil {
		slackPage(w, http.StatusNotFound, "Not linked", "<p>This org is no longer connected to Slack.</p>")
		return
	}
	person, err := slack.ExchangeUser(r.Context(), s.Slack.API, s.Slack.ClientID, s.Slack.ClientSecret, r.URL.Query().Get("code"), s.Slack.redirect())
	if err != nil {
		slackPage(w, http.StatusBadGateway, "Not linked", "<p>"+html.EscapeString(err.Error())+"</p>")
		return
	}
	if person.TeamID != inst.TeamID {
		slackPage(w, http.StatusForbidden, "Not linked", "<p>That Slack account is not in this org's workspace.</p>")
		return
	}
	cl, _, perr := s.slackClient(org)
	if perr != nil {
		slackPage(w, http.StatusBadGateway, "Not linked", "<p>"+html.EscapeString(perr.Message)+"</p>")
		return
	}
	name := s.userName(r.Context(), cl, org, person.ID)
	if err := s.store.AddSlackMember(SlackMember{Org: org, UserID: person.ID, Name: name, AddedBy: machine}); err != nil {
		slackPage(w, http.StatusInternalServerError, "Not linked", "<p>"+html.EscapeString(err.Error())+"</p>")
		return
	}
	s.log.Info("slack member linked", "org", org, "user", person.ID, "by", machine)
	slackPage(w, http.StatusOK, "You're connected",
		fmt.Sprintf("<p><b>%s</b> is linked to <b>%s</b>. Agents there can now reach you on Slack. You can also DM the app to message them.</p>"+
			"<p>You can close this tab.</p>", html.EscapeString(name), html.EscapeString(org)))
}
