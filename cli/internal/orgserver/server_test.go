package orgserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// fakeMachine is a machine's local agents, as a daemon would report them.
type fakeMachine struct {
	name   string
	mu     sync.Mutex
	agents []pool.Agent
	sent   []pool.LocalSend
	asked  []pool.LocalAsk
}

func (f *fakeMachine) Ask(_ context.Context, in pool.LocalAsk) (pool.AskResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, in)
	return pool.AskResult{ID: f.name + "/" + in.ID, Answer: "a copy of " + in.ID + " says: " + in.Question}, nil
}

func (f *fakeMachine) Agents(context.Context) ([]pool.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pool.Agent(nil), f.agents...), nil
}

func (f *fakeMachine) Read(_ context.Context, in pool.LocalRead) (pool.ReadResult, error) {
	for _, a := range f.agents {
		if a.ID == f.name+"/"+in.ID {
			entries := []pool.Entry{
				{Type: "user", Text: "fix the tests", Time: "2026-11-17T10:00:00Z"},
				{Type: "compacted", Trigger: "auto", Time: "2026-11-17T11:00:00Z"},
				{Type: "assistant", Text: "tests pass", Time: "2026-11-17T12:00:00Z"},
			}
			return pool.Narrow(a, entries, in.ReadOptions), nil
		}
	}
	return pool.ReadResult{}, pool.Errorf("not_found", "no live agent %s on this machine", in.ID)
}

func (f *fakeMachine) Send(_ context.Context, in pool.LocalSend) (pool.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, in)
	return pool.SendResult{ID: f.name + "/" + in.ID, Delivered: true, Via: "fake"}, nil
}

func agent(machine, session, cwd, role, root, active string) pool.Agent {
	return pool.Agent{ID: machine + "/" + session, Machine: machine, Kind: "claude", Role: role, Root: root, Status: "idle", Cwd: cwd, LastActive: active}
}

type org struct {
	t      *testing.T
	server *Server
	srv    *httptest.Server
	token  string
	cancel []context.CancelFunc
}

func newOrg(t *testing.T, exts ...extensions.Extension) *org {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "orgs.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.SetSealKey("token-key")
	s := New(store, "admin-secret", nil)
	s.CallTimeout = 5 * time.Second
	s.Extensions = exts
	mux := http.NewServeMux()
	s.Register(mux)
	o := &org{t: t, server: s, srv: httptest.NewServer(mux)}
	s.PublicURL = o.srv.URL
	t.Cleanup(func() {
		for _, c := range o.cancel {
			c()
		}
		o.srv.Close()
	})
	body, _ := json.Marshal(map[string]string{"name": "acme"})
	req, _ := http.NewRequest("POST", o.srv.URL+"/v1/orgs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create org: %v %v", resp.Status, err)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	o.token = out["join_token"]
	return o
}

// join adds a machine and starts its daemon.
func (o *org) join(local *fakeMachine) pool.Client {
	o.t.Helper()
	m, err := pool.Join(context.Background(), nil, o.srv.URL, o.token, local.name)
	if err != nil {
		o.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = append(o.cancel, cancel)
	go pool.Daemon{Membership: m, Local: local, ScanEvery: 20 * time.Millisecond}.Run(ctx)
	return pool.Client{Membership: m}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMachinesShareOnePool(t *testing.T) {
	o := newOrg(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{
		agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z"),
		agent("laptop", "cc-00000002", "/work/app", "subagent", "laptop/cc-00000001", "2026-11-17T12:00:00Z"),
		// A machine cannot report agents under another machine's name.
		agent("devbox", "cc-0000dead", "/x", "main", "", "2026-11-17T13:00:00Z"),
	}}
	devbox := &fakeMachine{name: "devbox", agents: []pool.Agent{
		agent("devbox", "cx-00000003", "/work/infra", "main", "", "2026-11-17T11:00:00Z"),
	}}
	c := o.join(laptop)
	o.join(devbox)
	ctx := context.Background()

	var list []pool.Agent
	waitFor(t, "both machines to report", func() bool {
		list, _ = c.List(ctx, pool.Selector{})
		return len(list) == 3
	})
	// The laptop's group is newest (its subagent was active at 12:00), so it sorts first.
	if list[0].ID != "laptop/cc-00000001" || list[1].ID != "laptop/cc-00000002" || list[2].ID != "devbox/cx-00000003" {
		t.Fatalf("list = %v", ids(list))
	}
	infra, _ := c.List(ctx, pool.Selector{Cwd: "/work/infra"})
	if len(infra) != 1 || infra[0].Machine != "devbox" {
		t.Fatalf("cwd filter = %v", ids(infra))
	}

	// read is relayed to the owning machine; by default it starts at the last compaction.
	res, err := c.Read(ctx, "devbox/cx-00000003", pool.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 2 || res.Run != 2 || len(res.Entries) != 2 || res.Entries[0].Type != "compacted" || res.Cursor != "2026-11-17T12:00:00Z" {
		t.Fatalf("read = %+v", res)
	}
	newer, err := c.Read(ctx, "devbox/cx-00000003", pool.ReadOptions{After: res.Cursor})
	if err != nil || len(newer.Entries) != 0 {
		t.Fatalf("read after cursor = %+v, %v", newer, err)
	}
	if _, err := c.Read(ctx, "devbox/cx-0000beef", pool.ReadOptions{}); code(err) != "not_found" {
		t.Fatalf("read of a missing agent: %v", err)
	}

	// send by ID, and to a group (main agents only).
	if _, err := c.Send(ctx, pool.SendRequest{Target: pool.Selector{ID: "devbox/cx-00000003"}, Text: "rebase", From: "laptop"}); err != nil {
		t.Fatal(err)
	}
	out, err := c.Send(ctx, pool.SendRequest{Target: pool.Selector{All: true}, Text: "standup in 5"})
	if err != nil || len(out.Results) != 2 {
		t.Fatalf("send all = %+v, %v", out, err)
	}
	if len(devbox.sent) != 2 || devbox.sent[0].From != "laptop" || len(laptop.sent) != 1 {
		t.Fatalf("delivered: devbox %v, laptop %v", devbox.sent, laptop.sent)
	}

	// The sender is the calling machine, whatever name the caller claims.
	if _, err := c.Send(ctx, pool.SendRequest{Target: pool.Selector{ID: "devbox/cx-00000003"}, Text: "deploy", From: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if got := devbox.sent[2].From; got != "devbox@laptop" {
		t.Fatalf("spoofed sender delivered as %q", got)
	}
	// A sending agent is named in the caller's namespace, so the reply goes back to it.
	for agent, want := range map[string]string{"cc-0000aaaa": "laptop/cc-0000aaaa", "devbox/cx-00000003": ""} {
		if _, err := c.Send(ctx, pool.SendRequest{Target: pool.Selector{ID: "devbox/cx-00000003"}, Text: "ping", Agent: agent}); err != nil {
			t.Fatal(err)
		}
		if got := devbox.sent[len(devbox.sent)-1].ReplyTo; got != want {
			t.Fatalf("agent %q: reply to %q, want %q", agent, got, want)
		}
	}
}

func TestDisconnectedMachineIsOfflineAndLeavingRevokes(t *testing.T) {
	o := newOrg(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/w", "main", "", "")}}
	devbox := &fakeMachine{name: "devbox", agents: []pool.Agent{agent("devbox", "cx-00000003", "/w", "main", "", "")}}
	c := o.join(laptop)
	dev := o.join(devbox)
	ctx := context.Background()
	waitFor(t, "both machines", func() bool { l, _ := c.List(ctx, pool.Selector{}); return len(l) == 2 })

	o.cancel[1]() // the devbox daemon stops
	waitFor(t, "devbox offline", func() bool {
		l, _ := c.List(ctx, pool.Selector{Machine: "devbox"})
		return len(l) == 1 && l[0].Status == pool.StatusOffline
	})
	if _, err := c.Send(ctx, pool.SendRequest{Target: pool.Selector{ID: "devbox/cx-00000003"}, Text: "hi"}); code(err) != "offline" {
		t.Fatalf("send to an offline machine: %v", err)
	}

	if err := dev.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dev.List(ctx, pool.Selector{}); code(err) != "unauthorized" {
		t.Fatalf("a machine that left still lists: %v", err)
	}
	// Its name is free again.
	if _, err := pool.Join(ctx, nil, o.srv.URL, o.token, "devbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Join(ctx, nil, o.srv.URL, o.token, "laptop"); code(err) != "exists" {
		t.Fatalf("joining under a taken name: %v", err)
	}
	if _, err := pool.Join(ctx, nil, o.srv.URL, "cdj_wrong", "other"); code(err) != "not_found" {
		t.Fatalf("joining with a bad token: %v", err)
	}
}

func TestCredentialOnlyWorksForItsOwnOrg(t *testing.T) {
	o := newOrg(t)
	c := o.join(&fakeMachine{name: "laptop"})
	other := c
	other.Org = "someone-else"
	if _, err := other.List(context.Background(), pool.Selector{}); code(err) != "unauthorized" {
		t.Fatalf("cross-org list: %v", err)
	}
	req, _ := http.NewRequest("POST", o.srv.URL+"/v1/orgs", bytes.NewReader([]byte(`{"name":"x"}`)))
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a machine credential created an org: %v %v", resp.Status, err)
	}
}

func ids(list []pool.Agent) []string {
	var out []string
	for _, a := range list {
		out = append(out, a.ID)
	}
	return out
}

func code(err error) string {
	var e *pool.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAskIsAnsweredOnTheMachineThatRunsTheAgent(t *testing.T) {
	o := newOrg(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/w", "main", "", "")}}
	o.join(laptop)
	dc := o.join(&fakeMachine{name: "devbox"})
	ctx := context.Background()
	waitFor(t, "laptop to report", func() bool { l, _ := dc.List(ctx, pool.Selector{}); return len(l) == 1 })

	res, err := dc.Ask(ctx, pool.AskRequest{ID: "laptop/cc-00000001", Question: "which parser?", Agent: "cc-00000009"})
	if err != nil || res.Answer != "a copy of cc-00000001 says: which parser?" {
		t.Fatalf("ask = %+v, %v", res, err)
	}
	if len(laptop.asked) != 1 || laptop.asked[0].From != "devbox" || laptop.asked[0].ReplyTo != "devbox/cc-00000009" {
		t.Fatalf("laptop was asked %+v", laptop.asked)
	}
	if _, err := dc.Ask(ctx, pool.AskRequest{ID: "laptop/cc-00000001"}); code(err) != "bad_request" {
		t.Fatalf("empty question: %v", err)
	}
}
