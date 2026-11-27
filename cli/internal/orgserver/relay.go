package orgserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"contextdrop.dev/context-drop/internal/pool"
)

// When servers share Postgres, a call for a machine connected to another server goes through the
// database: the caller writes the call to the relay table and notifies; the server holding the machine
// runs it, writes the result, and notifies back.

const relayChannel = "context_drop_relay"

// relay carries calls between servers. Its zero value (no DSN) relays nothing: one server.
type relay struct {
	s       *Server
	dsn     string
	mu      sync.Mutex
	waiting map[string]chan struct{}
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// listen keeps a connection listening for relay notifications until ctx ends, reconnecting on failure.
func (r *relay) listen(ctx context.Context) {
	for ctx.Err() == nil {
		if err := r.listenOnce(ctx); err != nil && ctx.Err() == nil {
			r.s.log.Warn("relay listener", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

func (r *relay) listenOnce(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, r.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+relayChannel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		kind, rest, _ := strings.Cut(n.Payload, "|")
		server, id, _ := strings.Cut(rest, "|")
		if server != r.s.ID {
			continue
		}
		switch kind {
		case "call":
			go r.serve(id)
		case "done":
			r.mu.Lock()
			ch := r.waiting[id]
			r.mu.Unlock()
			if ch != nil {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (r *relay) notify(ctx context.Context, payload string) error {
	_, err := r.s.store.db.ExecContext(ctx, `SELECT pg_notify(?, ?)`, relayChannel, payload)
	return err
}

// forward runs a call on the server holding the machine and waits for its result.
func (r *relay) forward(ctx context.Context, server string, timeout time.Duration, org, machine, method string, input json.RawMessage) (json.RawMessage, *pool.Error) {
	id := newID()
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	if r.waiting == nil {
		r.waiting = map[string]chan struct{}{}
	}
	r.waiting[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiting, id)
		r.mu.Unlock()
		_, _ = r.s.store.db.Exec(`DELETE FROM relay WHERE id = ?`, id)
	}()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := r.s.store.db.ExecContext(ctx, `INSERT INTO relay (id, from_server, to_server, org, machine, method, input, timeout_ms, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, r.s.ID, server, org, machine, method, string(input), timeout.Milliseconds(), time.Now().UnixMilli())
	if err != nil {
		return nil, pool.Errorf("internal", "relay: %v", err)
	}
	if err := r.notify(ctx, "call|"+server+"|"+id); err != nil {
		return nil, pool.Errorf("internal", "relay: %v", err)
	}
	// A notification can be missed while a listener reconnects, so also look every few seconds.
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			return nil, pool.Errorf("timeout", "machine %s did not answer within %s", machine, timeout)
		}
		var done bool
		var out, perr *string
		if err := r.s.store.db.QueryRowContext(ctx, `SELECT done, output, error FROM relay WHERE id = ?`, id).Scan(&done, &out, &perr); err != nil {
			if ctx.Err() != nil {
				return nil, pool.Errorf("timeout", "machine %s did not answer within %s", machine, timeout)
			}
			return nil, pool.Errorf("internal", "relay: %v", err)
		}
		if !done {
			continue
		}
		var e *pool.Error
		if perr != nil && *perr != "" {
			e = &pool.Error{}
			if json.Unmarshal([]byte(*perr), e) != nil {
				e = pool.Errorf("internal", "%s", *perr)
			}
		}
		var o json.RawMessage
		if out != nil {
			o = json.RawMessage(*out)
		}
		return o, e
	}
}

// serve runs a call another server relayed here, and hands back its result.
func (r *relay) serve(id string) {
	var from, org, machine, method, input string
	var timeoutMS int64
	err := r.s.store.db.QueryRow(`SELECT from_server, org, machine, method, input, timeout_ms FROM relay WHERE id = ? AND to_server = ? AND NOT done`,
		id, r.s.ID).Scan(&from, &org, &machine, &method, &input, &timeoutMS)
	if err != nil {
		return
	}
	ctx := context.Background()
	out, perr := r.s.callLocal(ctx, time.Duration(timeoutMS)*time.Millisecond, org, machine, method, json.RawMessage(input))
	var outS, errS *string
	if out != nil {
		v := string(out)
		outS = &v
	}
	if perr != nil {
		b, _ := json.Marshal(perr)
		v := string(b)
		errS = &v
	}
	if _, err := r.s.store.db.Exec(`UPDATE relay SET output = ?, error = ?, done = TRUE WHERE id = ?`, outS, errS, id); err != nil {
		r.s.log.Warn("relay result", "err", err)
		return
	}
	_ = r.notify(ctx, "done|"+from+"|"+id)
}
