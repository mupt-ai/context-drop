package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/orgserver"
	"contextdrop.dev/context-drop/internal/pool"
)

// Every test starts on a machine that has not joined an org, whatever the
// developer's own ~/.context-drop holds.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "context-drop-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("CONTEXT_DROP_HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

type remoteMachine struct {
	mu      sync.Mutex
	sent    []pool.LocalSend
	entries []pool.Entry
}

func (r *remoteMachine) Agents(context.Context) ([]pool.Agent, error) {
	return []pool.Agent{{ID: "devbox/cx-00000003", Machine: "devbox", Kind: "codex", Role: "main", Status: "idle",
		Cwd: "/work/infra", Name: "terraform plan", LastActive: time.Now().UTC().Format(time.RFC3339Nano)}}, nil
}

func (r *remoteMachine) Read(_ context.Context, in pool.LocalRead) (pool.ReadResult, error) {
	a, _ := r.Agents(context.Background())
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := append([]pool.Entry{{Type: "assistant", Text: "plan has 3 changes", Time: "2026-11-17T12:00:00Z"}}, r.entries...)
	return pool.Narrow(a[0], entries, in.ReadOptions), nil
}

// Send records the message, and the agent answers it.
func (r *remoteMachine) Send(_ context.Context, in pool.LocalSend) (pool.SendResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, in)
	now := time.Now().UTC()
	r.entries = append(r.entries,
		pool.Entry{Type: "user", Text: "[context-drop] From laptop.\n\n" + in.Text, Time: now.Format(time.RFC3339Nano)},
		pool.Entry{Type: "assistant", Text: "done: " + in.Text, Time: now.Add(time.Millisecond).Format(time.RFC3339Nano)})
	return pool.SendResult{ID: "devbox/" + in.ID, Delivered: true, Via: "codex queue"}, nil
}

func TestJoinThenListReadAndSendAcrossTheOrg(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	store, err := orgserver.OpenStore(filepath.Join(t.TempDir(), "orgs.json"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateOrg("acme")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	orgserver.New(store, "", nil).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Another machine joins and runs its daemon.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	devbox, err := pool.Join(ctx, nil, srv.URL, token, "devbox")
	if err != nil {
		t.Fatal(err)
	}
	remote := &remoteMachine{}
	go pool.Daemon{Membership: devbox, Local: remote, ScanEvery: 20 * time.Millisecond}.Run(ctx)

	out, _, err := executeRoot(t, "join", "--server", srv.URL, "--name", "laptop", token)
	if err != nil || !strings.Contains(out, "Joined org acme as laptop") {
		t.Fatalf("join: %q, %v", out, err)
	}
	if _, _, err := executeRoot(t, "join", "--server", srv.URL, token); err == nil || !strings.Contains(err.Error(), "already joined") {
		t.Fatalf("second join: %v", err)
	}

	var list []pool.Agent
	deadline := time.Now().Add(5 * time.Second)
	for len(list) == 0 && time.Now().Before(deadline) {
		out, _, err = executeRoot(t, "list", "--json", "--machine", "devbox")
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal([]byte(out), &list)
		time.Sleep(20 * time.Millisecond)
	}
	if len(list) != 1 || list[0].ID != "devbox/cx-00000003" {
		t.Fatalf("list = %s", out)
	}

	out, _, err = executeRoot(t, "read", "devbox/cx-00000003")
	if err != nil || !strings.Contains(out, "plan has 3 changes") {
		t.Fatalf("read: %q, %v", out, err)
	}

	_, stderr, err := executeRoot(t, "send", "devbox/cx-00000003", "apply", "it")
	if err != nil || !strings.Contains(stderr, "sent to devbox/cx-00000003 via codex queue") {
		t.Fatalf("send: %q, %v", stderr, err)
	}
	if len(remote.sent) != 1 || remote.sent[0].Text != "apply it" || remote.sent[0].From != "laptop" {
		t.Fatalf("delivered = %+v", remote.sent)
	}
	orgWaitEvery = 10 * time.Millisecond
	out, _, err = executeRoot(t, "send", "--wait", "--timeout", "5s", "devbox/cx-00000003", "run the plan")
	if err != nil || !strings.Contains(out, "done: run the plan") {
		t.Fatalf("send --wait: %q, %v", out, err)
	}

	out, _, err = executeRoot(t, "leave")
	if err != nil || !strings.Contains(out, "Left org acme") {
		t.Fatalf("leave: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CONTEXT_DROP_HOME"), "org.json")); !os.IsNotExist(err) {
		t.Fatalf("org.json still exists: %v", err)
	}
}

func TestOrgIDsAreNeverLocalIDs(t *testing.T) {
	for _, id := range []string{"cc-1234abcd", "sess-1/a17", "0b1c2d3e-aaaa-bbbb-cccc-1234567890ab", "laptop/cc-xyz", "Laptop/cc-1234abcd"} {
		if _, _, ok := pool.SplitID(id); ok {
			t.Errorf("%q parsed as an org ID", id)
		}
	}
	if m, s, ok := pool.SplitID("avyay-mbp/cc-1234abcd"); !ok || m != "avyay-mbp" || s != "cc-1234abcd" {
		t.Errorf("SplitID = %q %q %v", m, s, ok)
	}
	if _, _, err := executeRoot(t, "daemon"); err == nil || !strings.Contains(err.Error(), "--share") {
		t.Errorf("daemon without a share choice: %v", err)
	}
	var buf bytes.Buffer
	printEntries(&buf, []pool.Entry{{Type: "compacted", Trigger: "auto"}})
	if !strings.Contains(buf.String(), "compacted (auto)") {
		t.Errorf("entries = %q", buf.String())
	}
}

func TestShareDirsExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := shareDirs([]string{"~/work"})
	if err != nil || len(got) == 0 || got[0] != filepath.Join(home, "work") {
		t.Fatalf("shareDirs(~/work) = %v, %v; want %s first", got, err, filepath.Join(home, "work"))
	}
}
