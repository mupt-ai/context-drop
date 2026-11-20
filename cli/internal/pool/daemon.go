package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Daemon keeps this machine connected to its org: it reports the machine's
// agents and answers the calls the server relays to it.
type Daemon struct {
	Membership
	Local Local
	Log   *slog.Logger
	// ScanEvery is how often agents are rediscovered (default 2s); a changed
	// list is sent at once, an unchanged one every Heartbeat (default 30s).
	ScanEvery time.Duration
	Heartbeat time.Duration
}

// ErrRevoked means the server no longer accepts this machine's credential.
var ErrRevoked = errors.New("the org no longer accepts this machine; run `context-drop join` again")

// Run connects and reconnects until ctx ends or the credential is revoked.
func (d Daemon) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		connected, err := d.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrRevoked) {
			return err
		}
		if connected {
			backoff = time.Second
		}
		d.log().Warn("disconnected from org; retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (d Daemon) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// session runs one connection. connected reports whether it got as far as
// connecting, so a long session resets the reconnect backoff.
func (d Daemon) session(ctx context.Context) (connected bool, err error) {
	url := "ws" + strings.TrimPrefix(d.Server, "http") + "/v1/" + d.Org + "/connect"
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + d.Credential}},
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return false, ErrRevoked
		}
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	d.log().Info("connected to org", "org", d.Org, "machine", d.Machine)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.report(ctx, conn)
	for {
		var f Frame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			return true, err
		}
		if f.Type == "call" {
			go d.answer(ctx, conn, f)
		}
	}
}

// report sends the agent list whenever it changes, and at least every heartbeat.
func (d Daemon) report(ctx context.Context, conn *websocket.Conn) {
	scan, beat := d.ScanEvery, d.Heartbeat
	if scan <= 0 {
		scan = 2 * time.Second
	}
	if beat <= 0 {
		beat = 30 * time.Second
	}
	var last []byte
	var sent time.Time
	for {
		list, err := d.Local.Agents(ctx)
		if err != nil {
			d.log().Warn("listing local agents failed", "err", err)
		} else {
			if list == nil {
				list = []Agent{}
			}
			cur, _ := json.Marshal(list)
			if !bytes.Equal(cur, last) || time.Since(sent) >= beat {
				if err := wsjson.Write(ctx, conn, Frame{Type: "agents", Agents: list}); err != nil {
					return
				}
				last, sent = cur, time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(scan):
		}
	}
}

func (d Daemon) answer(ctx context.Context, conn *websocket.Conn, call Frame) {
	reply := Frame{Type: "result", ID: call.ID}
	out, err := d.dispatch(ctx, call)
	if err != nil {
		reply.Error = AsError(err)
	} else {
		reply.Output, _ = json.Marshal(out)
	}
	if err := wsjson.Write(ctx, conn, reply); err != nil {
		d.log().Warn("replying to the org failed", "call", call.Name, "err", err)
	}
}

func (d Daemon) dispatch(ctx context.Context, call Frame) (any, error) {
	switch call.Name {
	case "read":
		var in LocalRead
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return nil, err
		}
		return d.Local.Read(ctx, in)
	case "send":
		var in LocalSend
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return nil, err
		}
		return d.Local.Send(ctx, in)
	case "ask":
		var in LocalAsk
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return nil, err
		}
		return d.Local.Ask(ctx, in)
	}
	return nil, Errorf("unsupported", "this machine's Context Drop does not support %q; update it", call.Name)
}
