package orgserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/extensions/slack"
	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/pool"
)

// fakeSlack is the slice of the Web API the server uses, recording every call.
type fakeSlack struct {
	mu    sync.Mutex
	calls []fakeCall
	ts    int
}

type fakeCall struct {
	Method string
	Args   map[string]any
}

func (f *fakeSlack) called(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c.Args)
		}
	}
	return out
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/")
	args := map[string]any{}
	if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		_ = r.ParseForm()
		for k, v := range r.PostForm {
			args[k] = v[0]
		}
	} else if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&args)
	}
	for k, v := range r.URL.Query() {
		args[k] = v[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method, args})
	f.ts++
	ts := "1700000000.00010" + string(rune('0'+f.ts%10))
	f.mu.Unlock()
	user := map[string]any{"id": "U0000AVY", "name": "avyay", "profile": map[string]any{"display_name": "Avyay"}}
	var out map[string]any
	switch method {
	case "users.list":
		out = map[string]any{"members": []any{user, map[string]any{"id": "U0000BOB", "name": "bob"}}}
	case "users.info":
		out = map[string]any{"user": user}
	case "openid.connect.token":
		// A person signing in: the access token carries the code, so userInfo can tell whose it is.
		out = map[string]any{"access_token": "user-" + fmt.Sprint(args["code"])}
	case "openid.connect.userInfo":
		// "good" is someone in the org's workspace; anyone else is from another workspace.
		team := "T1"
		if r.Header.Get("Authorization") != "Bearer user-good" {
			team = "T-OTHER"
		}
		out = map[string]any{"sub": "U0000AVY", "name": "Avyay", "https://slack.com/team_id": team}
	case "conversations.list":
		out = map[string]any{"channels": []any{map[string]any{"id": "C0000DEP", "name": "deploys"}}}
	case "conversations.open":
		out = map[string]any{"channel": map[string]any{"id": "D0000AVY"}}
	case "chat.postMessage":
		out = map[string]any{"channel": args["channel"], "ts": ts}
	case "conversations.replies", "conversations.history":
		out = map[string]any{"messages": []any{
			map[string]any{"ts": "1700000002.000000", "user": "U0000AVY", "text": "ship it"},
			map[string]any{"ts": "1700000001.000000", "bot_id": "B1", "text": "may I ship?"},
		}}
	default:
		out = map[string]any{}
	}
	out["ok"] = true
	_ = json.NewEncoder(w).Encode(out)
}

// slackOrg is an org connected to a fake Slack workspace, with one laptop agent.
func slackOrg(t *testing.T) (*org, *fakeSlack, *fakeMachine, pool.Client) {
	o, fs, laptop, c, _ := slackOrgExt(t)
	return o, fs, laptop, c
}

func slackOrgExt(t *testing.T) (*org, *fakeSlack, *fakeMachine, pool.Client, *slack.Ext) {
	t.Helper()
	fs := &fakeSlack{}
	api := httptest.NewServer(fs)
	t.Cleanup(api.Close)
	ext := &slack.Ext{ClientID: "cid", ClientSecret: "csecret", SigningSecret: "signing", API: api.URL}
	o := newOrg(t, ext)
	if err := ext.Connect(slack.Workspace{Org: "acme", TeamID: "T1", TeamName: "Acme", BotUserID: "U0000BOT"}, "xoxb-secret"); err != nil {
		t.Fatal(err)
	}
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	c := o.join(laptop)
	waitFor(t, "the laptop's agents", func() bool { return len(o.server.agents("acme")) == 1 })
	return o, fs, laptop, c, ext
}

func (o *org) slackEvent(t *testing.T, id string, ev map[string]any) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"type": "event_callback", "team_id": "T1", "event_id": id, "event": ev})
	req, _ := http.NewRequest("POST", o.srv.URL+"/v1/slack/events", bytes.NewReader(body))
	for k, v := range slack.Sign("signing", body, time.Now()) {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (f *fakeMachine) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// restrict limits the org's agents to linked people, as `slack restrict` does.
func restrict(t *testing.T, c pool.Client) {
	t.Helper()
	if err := c.Post(context.Background(), "slack/restrict", map[string]bool{"restricted": true}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAgentAsksAPersonAndTheirThreadReplyComesBack(t *testing.T) {
	o, fs, laptop, c := slackOrg(t)
	ctx := context.Background()
	restrict(t, c)
	if err := c.Post(ctx, "slack/members", map[string]string{"op": "add", "user": "@avyay"}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := c.Send(ctx, pool.SendRequest{Slack: "#deploys", Text: "May I ship v2?", Agent: "cc-00000001"})
	if err != nil {
		t.Fatal(err)
	}
	thread := out.Results[0].ID
	if !strings.HasPrefix(thread, "slack:C0000DEP/") {
		t.Fatalf("send returned %q, want a thread in #deploys", thread)
	}
	posted := fs.called("chat.postMessage")
	if len(posted) != 1 || !strings.Contains(posted[0]["text"].(string), "laptop/cc-00000001") {
		t.Fatalf("posted %v, want the message naming the agent", posted)
	}
	ts := strings.TrimPrefix(thread, "slack:C0000DEP/")

	// A top-level channel message is pull-only: no agent hears it.
	o.slackEvent(t, "E1", map[string]any{"type": "message", "user": "U0000AVY", "text": "unrelated", "channel": "C0000DEP", "channel_type": "channel", "ts": "1700000009.000000"})
	// Someone who is not linked replies in the thread: ignored.
	o.slackEvent(t, "E2", map[string]any{"type": "message", "user": "U0000BOB", "text": "sure", "channel": "C0000DEP", "channel_type": "channel", "ts": "1700000010.000000", "thread_ts": ts})
	// A linked person replies in the thread: pushed to the agent, once, though Slack retries.
	for range 2 {
		if code := o.slackEvent(t, "E3", map[string]any{"type": "message", "user": "U0000AVY", "text": "yes, ship it", "channel": "C0000DEP", "channel_type": "channel", "ts": "1700000011.000000", "thread_ts": ts}); code != http.StatusOK {
			t.Fatalf("event status %d", code)
		}
	}
	waitFor(t, "the reply to reach the agent", func() bool { return laptop.sentCount() == 1 })
	time.Sleep(100 * time.Millisecond)
	laptop.mu.Lock()
	defer laptop.mu.Unlock()
	if len(laptop.sent) != 1 {
		t.Fatalf("agent got %d messages, want only the linked person's thread reply", len(laptop.sent))
	}
	got := laptop.sent[0]
	if got.ID != "cc-00000001" || !strings.Contains(got.Text, "yes, ship it") || !strings.Contains(got.Text, thread) || got.From != "Avyay on Slack" {
		t.Fatalf("agent got %+v", got)
	}
}

func TestDMGoesToTheAgentThatOpenedIt(t *testing.T) {
	o, _, laptop, c := slackOrg(t)
	ctx := context.Background()
	if err := c.Post(ctx, "slack/members", map[string]string{"op": "add", "user": "U0000AVY"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(ctx, pool.SendRequest{Slack: "@avyay", Text: "Which region?", Agent: "cc-00000001"}); err != nil {
		t.Fatal(err)
	}
	// A top-level DM reply, not in the thread, still reaches the agent that opened the DM.
	o.slackEvent(t, "E1", map[string]any{"type": "message", "user": "U0000AVY", "text": "us-east", "channel": "D0000AVY", "channel_type": "im", "ts": "1700000020.000000"})
	waitFor(t, "the DM to reach the agent", func() bool { return laptop.sentCount() == 1 })
}

func TestUnlinkedPeopleAreToldInsteadOfReachingAgentsWhenRestricted(t *testing.T) {
	o, fs, laptop, c := slackOrg(t)
	restrict(t, c)
	if _, err := c.Send(context.Background(), pool.SendRequest{Slack: "@avyay", Text: "hi", Agent: "cc-00000001"}); err != nil {
		t.Fatal(err)
	}
	o.slackEvent(t, "E1", map[string]any{"type": "message", "user": "U0000AVY", "text": "hello?", "channel": "D0000AVY", "channel_type": "im", "ts": "1700000030.000000"})
	waitFor(t, "a notice to the unlinked person", func() bool { return len(fs.called("chat.postMessage")) == 2 })
	if laptop.sentCount() != 0 {
		t.Fatal("an unlinked person's DM reached the agent")
	}
}

// connectLink starts a person's sign-in from the laptop and returns the state in its link.
func connectLink(t *testing.T, c pool.Client) string {
	t.Helper()
	var start struct{ URL string }
	if err := c.Post(context.Background(), "slack/connect", struct{}{}, &start); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func get(t *testing.T, link string) (int, string) {
	t.Helper()
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAPersonLinksThemselvesBySigningInWithSlack(t *testing.T) {
	o, _, _, c, ext := slackOrgExt(t)
	state := connectLink(t, c)

	// The link first says which org and machine asked, before anything goes to Slack.
	code, page := get(t, o.srv.URL+"/v1/slack/link?state="+url.QueryEscape(state))
	if code != http.StatusOK || !strings.Contains(page, "acme") || !strings.Contains(page, "laptop") || !strings.Contains(page, "slack.com/openid/connect/authorize") {
		t.Fatalf("link page %d: %s", code, page)
	}

	code, page = get(t, o.srv.URL+"/v1/slack/oauth?code=good&state="+url.QueryEscape(state))
	if code != http.StatusOK {
		t.Fatalf("callback %d: %s", code, page)
	}
	m, ok := ext.Member("acme", "U0000AVY")
	if !ok || m.Name != "Avyay" || m.AddedBy != "laptop" {
		t.Fatalf("member %+v %v, want Avyay linked by laptop", m, ok)
	}
}

func TestSigningInFromAnotherWorkspaceDoesNotLink(t *testing.T) {
	o, _, _, c, ext := slackOrgExt(t)
	state := connectLink(t, c)
	code, _ := get(t, o.srv.URL+"/v1/slack/oauth?code=elsewhere&state="+url.QueryEscape(state))
	if code != http.StatusForbidden {
		t.Fatalf("another workspace's sign-in: status %d, want 403", code)
	}
	if _, ok := ext.Member("acme", "U0000AVY"); ok {
		t.Fatal("a person from another workspace was linked")
	}
}

func TestATamperedLinkIsRefused(t *testing.T) {
	o, _, _, c, ext := slackOrgExt(t)
	state := connectLink(t, c)
	bad := "x" + state
	if code, _ := get(t, o.srv.URL+"/v1/slack/link?state="+url.QueryEscape(bad)); code != http.StatusBadRequest {
		t.Fatalf("tampered link page: status %d, want 400", code)
	}
	if code, _ := get(t, o.srv.URL+"/v1/slack/oauth?code=good&state="+url.QueryEscape(bad)); code != http.StatusBadRequest {
		t.Fatalf("tampered callback: status %d, want 400", code)
	}
	if _, ok := ext.Member("acme", "U0000AVY"); ok {
		t.Fatal("a tampered link linked someone")
	}
}

// modSend sends as a mod whose can list holds only the given capabilities.
func modSend(o *org, can []string, body string) *httptest.ResponseRecorder {
	grant := mods.Grant{Org: "acme", Mod: "standup", Can: map[string]bool{}}
	for _, c := range can {
		grant.Can[c] = true
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/acme/send", strings.NewReader(body))
	o.server.handleSend(w, r, caller{org: "acme", mod: &grant})
	return w
}

func TestAModWithOnlySlackCanPostToPeople(t *testing.T) {
	o, fs, _, _ := slackOrg(t)
	w := modSend(o, []string{"slack"}, `{"slack":"#deploys","text":"standup"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("a slack-only mod posting to Slack: %d %s", w.Code, w.Body)
	}
	var out pool.SendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Results) != 1 || out.Results[0].TS == "" {
		t.Fatalf("result %s, want the posted message's timestamp", w.Body)
	}
	if len(fs.called("chat.postMessage")) != 1 {
		t.Fatal("nothing was posted")
	}
	// Slack does not open agents to it: messaging an agent still needs send.
	if w := modSend(o, []string{"slack"}, `{"target":{"id":"laptop/cc-00000001"},"text":"hi"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a slack-only mod messaging an agent: %d, want 403", w.Code)
	}
	// And send alone does not open Slack.
	if w := modSend(o, []string{"send"}, `{"slack":"#deploys","text":"hi"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a send-only mod posting to Slack: %d, want 403", w.Code)
	}
}

func TestEventsNeedSlacksSignature(t *testing.T) {
	o, _, _, _ := slackOrg(t)
	resp, err := http.Post(o.srv.URL+"/v1/slack/events", "application/json", strings.NewReader(`{"type":"url_verification","challenge":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned event: status %d, want 401", resp.StatusCode)
	}
}

func TestSlackReadIsOldestFirstAndMarksTheApp(t *testing.T) {
	_, _, _, c := slackOrg(t)
	res, err := c.SlackRead(context.Background(), pool.SlackReadRequest{Target: "#deploys"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 2 || !res.Messages[0].Bot || res.Messages[1].Name != "Avyay" {
		t.Fatalf("read %+v", res.Messages)
	}
}

func TestSlackAPIRefusesAdminMethods(t *testing.T) {
	for _, m := range []string{"admin.users.remove", "auth.revoke", "chat.delete", "apps.uninstall"} {
		if slack.MethodAllowed(m) {
			t.Errorf("%s is allowed", m)
		}
	}
	for _, m := range []string{"chat.postMessage", "reactions.add", "conversations.history", "users.info"} {
		if !slack.MethodAllowed(m) {
			t.Errorf("%s is refused", m)
		}
	}
}

func TestBotTokenIsSealedAtRest(t *testing.T) {
	o, _, _, _ := slackOrg(t)
	var raw []byte
	_ = o.server.store.EachExtension("slack", func(_ string, data json.RawMessage) error { raw = append(raw, data...); return nil })
	if len(raw) == 0 || bytes.Contains(raw, []byte("xoxb-secret")) {
		t.Fatal("the bot token is stored in the clear")
	}
}

func TestAnyoneInTheWorkspaceReachesAgentsByDefault(t *testing.T) {
	o, _, laptop, c := slackOrg(t)
	// Nobody is linked, and the org is not restricted.
	if _, err := c.Send(context.Background(), pool.SendRequest{To: "@avyay", Text: "Which region?", Agent: "cc-00000001"}); err != nil {
		t.Fatal(err)
	}
	o.slackEvent(t, "E1", map[string]any{"type": "message", "user": "U0000AVY", "text": "us-east", "channel": "D0000AVY", "channel_type": "im", "ts": "1700000020.000000"})
	waitFor(t, "an unlinked teammate's DM to reach the agent", func() bool { return laptop.sentCount() == 1 })
	laptop.mu.Lock()
	defer laptop.mu.Unlock()
	if got := laptop.sent[0]; !strings.Contains(got.Text, "us-east") || got.From != "Avyay on Slack" {
		t.Fatalf("agent got %+v", got)
	}
}
