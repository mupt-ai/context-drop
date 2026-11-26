package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/extensions"
)

func (e *Ext) redirect() string {
	return strings.TrimRight(e.host.PublicURL(), "/") + "/v1/linear/oauth"
}

// state signs which org and machine started an install, and when the request expires.
func (e *Ext) state(org, machine string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(org + "\n" + machine + "\n" + strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, []byte(e.ClientSecret))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (e *Ext) checkState(state string) (org, machine string, ok bool) {
	payload, sig, found := strings.Cut(state, ".")
	if !found {
		return "", "", false
	}
	mac := hmac.New(sha256.New, []byte(e.ClientSecret))
	mac.Write([]byte(payload))
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\n")
	if len(parts) != 3 {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().After(time.Unix(exp, 0)) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// handleOAuth finishes an install: Linear sends the admin back with a code, which becomes the app's
// token for that workspace.
func (e *Ext) handleOAuth(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string) {
		extensions.Page(w, status, "Linear not connected", "<p>"+html.EscapeString(msg)+"</p>")
	}
	if !e.oauth() {
		fail(http.StatusNotImplemented, "This server has no Linear app.")
		return
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		fail(http.StatusBadRequest, "Linear said: "+msg)
		return
	}
	org, machine, ok := e.checkState(r.URL.Query().Get("state"))
	if !ok {
		fail(http.StatusBadRequest, "The link expired. Run `context-drop linear install` again.")
		return
	}
	tok, err := Exchange(r.Context(), e.TokenURL, url.Values{"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")},
		"redirect_uri": {e.redirect()}, "client_id": {e.ClientID}, "client_secret": {e.ClientSecret}})
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	who, err := (Client{Auth: "Bearer " + tok.Access, URL: e.API}).Whoami(r.Context())
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	in := Install{Org: org, LinearOrg: who.OrgID, Name: who.OrgName, UserID: who.UserID, UserName: who.UserName, InstalledBy: machine, InstalledAt: time.Now().UTC()}
	if err := e.storeToken(&in, tok); err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	old, hadOld := e.install(org)
	if err := e.saveInstall(in); err != nil {
		if errors.Is(err, errTaken) {
			fail(http.StatusConflict, "Linear workspace "+who.OrgName+" is already connected to another org.")
			return
		}
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	if hadOld {
		e.deleteWebhook(r.Context(), old) // an earlier API key install's webhook would deliver twice
	}
	e.host.Log().Info("linear connected", "org", org, "workspace", who.OrgID, "by", machine)
	extensions.Page(w, http.StatusOK, "Linear connected", fmt.Sprintf("<p>Linear workspace <b>%s</b> is connected to org <b>%s</b>. Agents can comment on issues now, and replies come back to them.</p><p>You can close this tab.</p>",
		html.EscapeString(who.OrgName), html.EscapeString(org)))
}

// ExpireTokenForTest marks an org's OAuth token as expired, so the next call refreshes it.
func (e *Ext) ExpireTokenForTest(org string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.st.Installs {
		if e.st.Installs[i].Org == org {
			e.st.Installs[i].Expires = time.Now().Add(-time.Minute)
		}
	}
}
