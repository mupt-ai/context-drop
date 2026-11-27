package orgserver

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/pool"
)

// twoServers is one org served by two servers sharing a Postgres database, as behind a load balancer.
func twoServers(t *testing.T) (a, b *org) {
	t.Helper()
	if os.Getenv("CONTEXT_DROP_TEST_POSTGRES") == "" {
		t.Skip("set CONTEXT_DROP_TEST_POSTGRES to run tests against Postgres")
	}
	addr := testDatabase(t)
	open := func() *Store {
		st, err := OpenStore(addr)
		if err != nil {
			t.Fatal(err)
		}
		st.SetSealKey("token-key")
		t.Cleanup(func() { st.Close() })
		return st
	}
	a = newOrgOn(t, open())
	b = newOrgOn(t, open())
	b.token = a.token
	return a, b
}

// on is a client for the same machine that talks to another server.
func on(c pool.Client, o *org) pool.Client {
	c.Membership.Server = o.srv.URL
	return c
}

func TestAMachineOnOneServerIsReachedThroughAnother(t *testing.T) {
	a, b := twoServers(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	devbox := &fakeMachine{name: "devbox", agents: []pool.Agent{agent("devbox", "cc-00000002", "/work/api", "main", "", "2026-11-17T11:00:00Z")}}
	cl := a.join(laptop) // connected to server A
	b.join(devbox)       // connected to server B
	waitFor(t, "both servers to see both machines", func() bool {
		return len(a.server.agents("acme")) == 2 && len(b.server.agents("acme")) == 2
	})

	// Through server B, read and message the laptop's agent, which is connected to server A.
	viaB := on(cl, b)
	res, err := viaB.Read(context.Background(), "laptop/cc-00000001", pool.ReadOptions{})
	if err != nil || len(res.Entries) == 0 {
		t.Fatalf("read through B: %+v %v", res, err)
	}
	if _, err := viaB.Send(context.Background(), pool.SendRequest{Target: pool.Selector{ID: "laptop/cc-00000001"}, Text: "hi from B"}); err != nil {
		t.Fatal(err)
	}
	if laptop.sentCount() != 1 {
		t.Fatalf("laptop got %d messages", laptop.sentCount())
	}
	// And the other way: through A, to the devbox on B.
	if _, err := cl.Send(context.Background(), pool.SendRequest{Target: pool.Selector{ID: "devbox/cc-00000002"}, Text: "hi from A"}); err != nil {
		t.Fatal(err)
	}
	if devbox.sentCount() != 1 {
		t.Fatal("devbox got nothing")
	}
}

func TestAStoppedServersMachinesGoOffline(t *testing.T) {
	a, b := twoServers(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	a.join(laptop)
	waitFor(t, "B to see the laptop", func() bool { return len(b.server.agents("acme")) == 1 })
	if err := a.server.store.Stopped(a.server.ID); err != nil {
		t.Fatal(err)
	}
	if got := b.server.agents("acme"); got[0].Status != pool.StatusOffline {
		t.Fatalf("after A stopped, B sees %+v", got)
	}
}

func TestServersNeverLoseEachOthersExtensionUpdates(t *testing.T) {
	a, b := twoServers(t)
	type counter struct{ N int }
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := a.server.store
			if i%2 == 1 {
				st = b.server.store
			}
			var c counter
			if err := st.UpdateExtension(context.Background(), "test", "acme", &c, func() error { c.N++; return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var c counter
	if _, err := a.server.store.GetExtension("test", "acme", &c); err != nil || c.N != 20 {
		t.Fatalf("counter %d, %v; want 20", c.N, err)
	}
	if err := a.server.store.ClaimKey("slack", "T1", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := b.server.store.ClaimKey("slack", "T1", "other"); err != ErrTaken {
		t.Fatalf("a second org claimed T1: %v", err)
	}
	if !a.server.store.Once("slack", "E1") || b.server.store.Once("slack", "E1") {
		t.Fatal("an event was handled twice across servers")
	}
}

func TestARelayedCallWhoseNotificationWasMissedStillRuns(t *testing.T) {
	a, b := twoServers(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	a.join(laptop)
	waitFor(t, "B to see the laptop", func() bool { return len(b.server.agents("acme")) == 1 })
	// Write the call for A without notifying it, as if A's listener was reconnecting.
	in, _ := json.Marshal(pool.LocalSend{ID: "cc-00000001", Text: "missed"})
	if _, err := b.server.store.db.Exec(`INSERT INTO relay (id, from_server, to_server, org, machine, method, input, timeout_ms, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "missed-1", b.server.ID, a.server.ID, "acme", "laptop", "send", string(in), 5000, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A to pick up the call", func() bool { return laptop.sentCount() == 1 })
}

func TestOnlyOneServerWinsAKeyThatWasAbsent(t *testing.T) {
	a, b := twoServers(t)
	ma, err := mods.OpenStore(a.server.store.DB())
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := mods.OpenStore(b.server.store.DB())
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := ma
			if i%2 == 1 {
				st = mb
			}
			if _, ok, err := st.KV("acme", "lock", "cas", "held", json.RawMessage(`"me"`), nil); err != nil {
				t.Error(err)
			} else if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d callers took the lock", wins.Load())
	}
}

func TestACallFollowsAMachineThatReconnectedToAnotherServer(t *testing.T) {
	a, b := twoServers(t)
	old := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z")}}
	cl := a.join(old)
	waitFor(t, "A to hold the laptop", func() bool { s, ok, _ := a.server.store.Where("acme", "laptop"); return ok && s == a.server.ID })
	// The laptop reconnects through B while its socket on A has not failed yet.
	now := &fakeMachine{name: "laptop", agents: old.agents}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pool.Daemon{Membership: on(cl, b).Membership, Local: now, ScanEvery: 20 * time.Millisecond}.Run(ctx)
	waitFor(t, "B to hold the laptop", func() bool { s, ok, _ := a.server.store.Where("acme", "laptop"); return ok && s == b.server.ID })

	if _, err := cl.Send(context.Background(), pool.SendRequest{Target: pool.Selector{ID: "laptop/cc-00000001"}, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if now.sentCount() != 1 || old.sentCount() != 0 {
		t.Fatalf("live connection got %d, stale one %d", now.sentCount(), old.sentCount())
	}
}
