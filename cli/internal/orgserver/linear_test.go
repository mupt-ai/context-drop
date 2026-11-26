package orgserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/extensions/linear"
	"contextdrop.dev/context-drop/internal/extensions/slack"
	"contextdrop.dev/context-drop/internal/pool"
)

// fakeLinear is the slice of Linear's GraphQL API the extension uses.
type fakeLinear struct {
	mu       sync.Mutex
	comments []string
	secret   string
	auth     []string // the Authorization header of each GraphQL call
	grants   []string // the grant type of each token request
}

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/oauth/token" {
		_ = r.ParseForm()
		f.mu.Lock()
		f.grants = append(f.grants, r.PostForm.Get("grant_type"))
		n := len(f.grants)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + string(rune('0'+n)), "refresh_token": "ref", "expires_in": 86399})
		return
	}
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	var in struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	var data any
	switch q := in.Query; {
	case strings.Contains(q, "viewer"):
		data = map[string]any{"viewer": map[string]any{"id": "u-bot", "name": "Ops Bot"}, "organization": map[string]any{"id": "lin-org", "name": "Acme"}}
	case strings.Contains(q, "webhookCreate"):
		input := in.Variables["input"].(map[string]any)
		f.mu.Lock()
		f.secret = input["secret"].(string)
		f.mu.Unlock()
		data = map[string]any{"webhookCreate": map[string]any{"success": true, "webhook": map[string]any{"id": "hook-1"}}}
	case strings.Contains(q, "commentCreate"):
		input := in.Variables["input"].(map[string]any)
		f.mu.Lock()
		f.comments = append(f.comments, input["body"].(string))
		n := len(f.comments)
		f.mu.Unlock()
		data = map[string]any{"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "c-" + string(rune('0'+n)), "url": "https://linear.app/x"}}}
	case strings.Contains(q, "issue("):
		data = map[string]any{"issue": map[string]any{"id": "issue-uuid", "identifier": "ENG-1", "title": "Flaky deploy", "url": "https://linear.app/acme/issue/ENG-1"}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// linearOrg is an org connected to a fake Linear workspace, with one laptop agent.
func linearOrg(t *testing.T) (*org, *fakeLinear, *fakeMachine, pool.Client) {
	t.Helper()
	fl := &fakeLinear{}
	api := httptest.NewServer(fl)
	t.Cleanup(api.Close)
	o := newOrg(t, &linear.Ext{API: api.URL})
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	c := o.join(laptop)
	waitFor(t, "the laptop's agents", func() bool { return len(o.server.agents("acme")) == 1 })
	if err := c.Post(context.Background(), "linear/install", map[string]string{"key": "lin_api_x"}, nil); err != nil {
		t.Fatal(err)
	}
	return o, fl, laptop, c
}

// comment delivers a signed comment webhook and returns the status.
func (o *org) comment(t *testing.T, secret, id, body string) int {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"action": "create", "type": "Comment", "organizationId": "lin-org", "webhookTimestamp": time.Now().UnixMilli(),
		"actor": map[string]any{"name": "Sam"}, "data": map[string]any{"id": id, "body": body, "issueId": "issue-uuid"}})
	req, _ := http.NewRequest("POST", o.srv.URL+"/v1/linear/events", bytes.NewReader(raw))
	for k, v := range linear.Sign(secret, raw) {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAgentCommentsOnAnIssueAndAPersonsReplyComesBack(t *testing.T) {
	o, fl, laptop, c := linearOrg(t)
	out, err := c.Send(context.Background(), pool.SendRequest{To: "linear:ENG-1", Text: "Which region?", Agent: "cc-00000001"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Results[0].ID != "linear:ENG-1" || len(fl.comments) != 1 || !strings.Contains(fl.comments[0], "laptop/cc-00000001") {
		t.Fatalf("sent %+v, comments %q", out.Results, fl.comments)
	}
	// The agent's own comment, echoed back by the webhook, never reaches it.
	o.comment(t, fl.secret, "c-1", fl.comments[0])
	if code := o.comment(t, fl.secret, "c-9", "us-east-1, please"); code != http.StatusOK {
		t.Fatalf("webhook status %d", code)
	}
	waitFor(t, "the reply to reach the agent", func() bool { return laptop.sentCount() == 1 })
	time.Sleep(100 * time.Millisecond)
	laptop.mu.Lock()
	defer laptop.mu.Unlock()
	if len(laptop.sent) != 1 || !strings.Contains(laptop.sent[0].Text, "us-east-1") || !strings.Contains(laptop.sent[0].Text, "linear:ENG-1") || laptop.sent[0].From != "Sam on Linear" {
		t.Fatalf("agent got %+v", laptop.sent)
	}
}

func TestLinearWebhooksNeedTheirSignature(t *testing.T) {
	o, _, laptop, c := linearOrg(t)
	if _, err := c.Send(context.Background(), pool.SendRequest{To: "linear:ENG-1", Text: "hi", Agent: "cc-00000001"}); err != nil {
		t.Fatal(err)
	}
	if code := o.comment(t, "wrong-secret", "c-9", "do something"); code != http.StatusUnauthorized {
		t.Fatalf("unsigned webhook: status %d, want 401", code)
	}
	if laptop.sentCount() != 0 {
		t.Fatal("an unsigned webhook reached the agent")
	}
}

func TestAModWithOnlyLinearCanComment(t *testing.T) {
	o, fl, _, _ := linearOrg(t)
	if w := modSend(o, []string{"linear"}, `{"to":"linear:ENG-1","text":"nightly report"}`); w.Code != http.StatusOK {
		t.Fatalf("a linear-only mod commenting: %d %s", w.Code, w.Body)
	}
	if len(fl.comments) != 1 {
		t.Fatal("nothing was commented")
	}
	if w := modSend(o, []string{"slack"}, `{"to":"linear:ENG-1","text":"hi"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a slack-only mod commenting on Linear: %d, want 403", w.Code)
	}
}

// TestSlackStateFromBeforeExtensionsStillWorks opens an org file in the shape the server wrote before
// Slack became an extension, and checks Slack still knows the org's workspace and people.
func TestSlackStateFromBeforeExtensionsStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orgs.json")
	sealer, _ := OpenStore(filepath.Join(t.TempDir(), "x.json"))
	sealer.SetSealKey("token-key")
	sealed, err := sealer.Seal("xoxb-old")
	if err != nil {
		t.Fatal(err)
	}
	old := map[string]any{
		"orgs":           []any{map[string]any{"id": "acme", "join_token_hash": "h"}},
		"machines":       []any{},
		"slack_installs": []any{map[string]any{"org": "acme", "team_id": "T1", "team_name": "Acme", "bot_user_id": "U0000BOT", "token_sealed": sealed}},
		"slack_members":  []any{map[string]any{"org": "acme", "user_id": "U0000AVY", "name": "Avyay"}},
		"slack_routes":   []any{map[string]any{"org": "acme", "channel": "D1", "agents": []string{"laptop/cc-00000001"}, "at": time.Now()}},
	}
	raw, _ := json.Marshal(old)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.SetSealKey("token-key")
	ext := &slack.Ext{ClientID: "cid", ClientSecret: "cs", SigningSecret: "ss"}
	s := New(store, "", nil)
	s.Extensions = append(s.Extensions, ext)
	s.Register(http.NewServeMux())
	if m, ok := ext.Member("acme", "U0000AVY"); !ok || m.Name != "Avyay" {
		t.Fatalf("member %+v %v, want Avyay carried over", m, ok)
	}
	if !store.HasOrg("acme") {
		t.Fatal("org lost")
	}
	// The next save writes the new shape, with the token still sealed.
	if err := ext.Connect(slack.Workspace{Org: "acme", TeamID: "T1", TeamName: "Acme"}, "xoxb-new"); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if bytes.Contains(saved, []byte("slack_installs")) || !bytes.Contains(saved, []byte(`"extensions"`)) || bytes.Contains(saved, []byte("xoxb-")) {
		t.Fatalf("saved file: %s", saved)
	}
}

func TestAnAdminConnectsLinearByApprovingTheApp(t *testing.T) {
	fl := &fakeLinear{}
	api := httptest.NewServer(fl)
	t.Cleanup(api.Close)
	ext := &linear.Ext{ClientID: "lin-cid", ClientSecret: "lin-cs", WebhookSecret: "app-hook", API: api.URL, TokenURL: api.URL + "/oauth/token"}
	o := newOrg(t, ext)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	c := o.join(laptop)
	waitFor(t, "the laptop's agents", func() bool { return len(o.server.agents("acme")) == 1 })

	var start struct{ URL string }
	if err := c.Post(context.Background(), "linear/install", map[string]string{}, &start); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.URL)
	if u.Host != "linear.app" || u.Query().Get("actor") != "app" {
		t.Fatalf("install link %s, want Linear's approval page acting as the app", start.URL)
	}
	if code, _ := get(t, o.srv.URL+"/v1/linear/oauth?code=c1&state=x"+url.QueryEscape(u.Query().Get("state"))); code != http.StatusBadRequest {
		t.Fatalf("tampered state: %d, want 400", code)
	}
	if code, page := get(t, o.srv.URL+"/v1/linear/oauth?code=c1&state="+url.QueryEscape(u.Query().Get("state"))); code != http.StatusOK {
		t.Fatalf("callback %d: %s", code, page)
	}

	// Agents comment with the app's token, and replies signed with the app's secret come back.
	if _, err := c.Send(context.Background(), pool.SendRequest{To: "linear:ENG-1", Text: "Which region?", Agent: "cc-00000001"}); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	lastAuth := fl.auth[len(fl.auth)-1]
	fl.mu.Unlock()
	if lastAuth != "Bearer tok-1" {
		t.Fatalf("commented with %q, want the app's token", lastAuth)
	}
	o.comment(t, "app-hook", "c-9", "us-east-1")
	waitFor(t, "the reply to reach the agent", func() bool { return laptop.sentCount() == 1 })

	// A token about to expire is refreshed before the next call.
	ext.ExpireTokenForTest("acme")
	if _, err := c.Send(context.Background(), pool.SendRequest{To: "linear:ENG-1", Text: "Thanks", Agent: "cc-00000001"}); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if len(fl.grants) != 2 || fl.grants[1] != "refresh_token" || fl.auth[len(fl.auth)-1] != "Bearer tok-2" {
		t.Fatalf("grants %v, last auth %q: want a refresh and the new token", fl.grants, fl.auth[len(fl.auth)-1])
	}
}
