// Package slack is the small part of the Slack platform Context Drop uses: Web API calls with a bot
// token, the OAuth v2 install exchange, request signature checks, and the app manifest.
package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is Slack's Web API base.
const DefaultAPI = "https://slack.com/api"

// Error is a Slack API error such as channel_not_found.
type Error struct {
	Method string
	Code   string
}

func (e *Error) Error() string { return fmt.Sprintf("slack %s: %s", e.Method, e.Code) }

// Client calls the Web API with one bot token.
type Client struct {
	Token string
	// Base is the Web API base URL; empty means DefaultAPI.
	Base string
	HTTP *http.Client
}

func (c Client) base() string {
	if c.Base == "" {
		return DefaultAPI
	}
	return strings.TrimRight(c.Base, "/")
}

func (c Client) http() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return c.HTTP
}

// Call runs one Web API method with JSON arguments and returns the whole response.
func (c Client) Call(ctx context.Context, method string, args map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return c.do(req, method)
}

// Get runs a read method with query arguments. Some read methods (conversations.history, users.list)
// take only form or query arguments.
func (c Client) Get(ctx context.Context, method string, args url.Values) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/"+method+"?"+args.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return c.do(req, method)
}

func (c Client) do(req *http.Request, method string) (json.RawMessage, error) {
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &Error{Method: method, Code: "ratelimited (retry after " + resp.Header.Get("Retry-After") + "s)"}
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("slack %s: %s: %s", method, resp.Status, strings.TrimSpace(string(raw)))
	}
	if !head.OK {
		return nil, &Error{Method: method, Code: head.Error}
	}
	return raw, nil
}

// Install is what an OAuth v2 install returns.
type Install struct {
	Token     string
	TeamID    string
	TeamName  string
	BotUserID string
	// InstallerID is the person who approved the install.
	InstallerID string
}

// Exchange trades an OAuth code for the workspace's bot token.
func Exchange(ctx context.Context, base, clientID, clientSecret, code, redirect string) (Install, error) {
	if base == "" {
		base = DefaultAPI
	}
	form := url.Values{"code": {code}, "redirect_uri": {redirect}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/oauth.v2.access", strings.NewReader(form.Encode()))
	if err != nil {
		return Install{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	raw, err := (Client{}).do(req, "oauth.v2.access")
	if err != nil {
		return Install{}, err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		BotUserID   string `json:"bot_user_id"`
		AuthedUser  struct {
			ID string `json:"id"`
		} `json:"authed_user"`
		Team struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"team"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Install{}, err
	}
	if out.AccessToken == "" || out.Team.ID == "" {
		return Install{}, errors.New("slack oauth.v2.access returned no bot token")
	}
	return Install{Token: out.AccessToken, TeamID: out.Team.ID, TeamName: out.Team.Name, BotUserID: out.BotUserID, InstallerID: out.AuthedUser.ID}, nil
}

// Scopes are the bot scopes the app asks for.
var Scopes = []string{
	"app_mentions:read",
	"channels:history", "channels:read", "channels:join",
	"groups:history", "groups:read",
	"im:history", "im:read", "im:write",
	"mpim:history", "mpim:read",
	"chat:write", "chat:write.public",
	"users:read", "users:read.email",
	"reactions:read", "reactions:write",
	"pins:read", "pins:write",
	"files:read",
}

// AuthorizeURL is where an admin goes to install the app into a workspace.
func AuthorizeURL(clientID, redirect, state string) string {
	q := url.Values{"client_id": {clientID}, "scope": {strings.Join(Scopes, ",")}, "redirect_uri": {redirect}, "state": {state}}
	return "https://slack.com/oauth/v2/authorize?" + q.Encode()
}

// UserAuthorizeURL is where a person signs in with Slack (OpenID Connect), so the server learns who they
// are. It asks for identity only; the bot is not involved.
func UserAuthorizeURL(clientID, redirect, state string) string {
	q := url.Values{"response_type": {"code"}, "scope": {"openid profile"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {state}}
	return "https://slack.com/openid/connect/authorize?" + q.Encode()
}

// Person is a Slack user who signed in.
type Person struct {
	ID     string
	TeamID string
	Name   string
}

// ExchangeUser trades a sign-in code for the signed-in person's identity: their user ID, workspace, and
// name, from Slack's OpenID Connect token and userInfo endpoints. The access token is used once and not kept.
func ExchangeUser(ctx context.Context, base, clientID, clientSecret, code, redirect string) (Person, error) {
	if base == "" {
		base = DefaultAPI
	}
	base = strings.TrimRight(base, "/")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {clientID}, "client_secret": {clientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openid.connect.token", strings.NewReader(form.Encode()))
	if err != nil {
		return Person{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := (Client{}).do(req, "openid.connect.token")
	if err != nil {
		return Person{}, err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return Person{}, err
	}
	if tok.AccessToken == "" {
		return Person{}, errors.New("slack returned no sign-in token")
	}
	info, err := Client{Token: tok.AccessToken, Base: base}.Get(ctx, "openid.connect.userInfo", url.Values{})
	if err != nil {
		return Person{}, err
	}
	var who struct {
		Sub    string `json:"sub"`
		Name   string `json:"name"`
		TeamID string `json:"https://slack.com/team_id"`
	}
	if err := json.Unmarshal(info, &who); err != nil {
		return Person{}, err
	}
	if who.Sub == "" || who.TeamID == "" {
		return Person{}, errors.New("slack did not say who signed in")
	}
	return Person{ID: who.Sub, TeamID: who.TeamID, Name: who.Name}, nil
}

// MaxSkew is how old a signed request may be.
const MaxSkew = 5 * time.Minute

// Verify checks a request's Slack signature (v0) against the app's signing secret.
func Verify(secret string, h http.Header, body []byte, now time.Time) error {
	if secret == "" {
		return errors.New("no signing secret is configured")
	}
	ts := h.Get("X-Slack-Request-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("missing request timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > MaxSkew || d < -MaxSkew {
		return errors.New("request timestamp is too old")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:%s", ts, body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(h.Get("X-Slack-Signature"))) {
		return errors.New("bad signature")
	}
	return nil
}

// Sign makes the signature headers Verify accepts, for tests and local tools.
func Sign(secret string, body []byte, now time.Time) http.Header {
	ts := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:%s", ts, body)
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", ts)
	h.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

// Manifest is the Slack app manifest (YAML) for a Context Drop server at base.
func Manifest(name, base string) string {
	base = strings.TrimRight(base, "/")
	var scopes strings.Builder
	for _, s := range Scopes {
		fmt.Fprintf(&scopes, "      - %s\n", s)
	}
	return fmt.Sprintf(`display_information:
  name: %s
  description: Lets the org's coding agents message people, and people answer them.
features:
  app_home:
    home_tab_enabled: false
    messages_tab_enabled: true
    messages_tab_read_only_enabled: false
  bot_user:
    display_name: %s
    always_online: true
oauth_config:
  redirect_urls:
    - %s/v1/slack/oauth
  scopes:
    bot:
%s    user:
      - openid
      - profile
settings:
  event_subscriptions:
    request_url: %s/v1/slack/events
    bot_events:
      - message.channels
      - message.groups
      - message.im
      - message.mpim
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
`, name, name, base, scopes.String(), base)
}
