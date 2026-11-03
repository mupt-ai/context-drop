package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// openCodeProvider talks to the OpenCode background service through
// `opencode api`. A session is live while it is running, or while an OpenCode
// client process in the session's directory has viewed it since that client
// started.
type openCodeProvider struct{}

func (openCodeProvider) Kind() string { return OpenCode }

type openCodeSession struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	Title    string `json:"title"`
	Time     struct {
		Created float64 `json:"created"`
		Updated float64 `json:"updated"`
		Viewed  float64 `json:"viewed"`
	} `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

func (p openCodeProvider) api(ctx context.Context, env *Env, out any, method, path string, body any) error {
	args := []string{"api", method, path}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		args = append(args, "-d", string(data))
	}
	raw, err := env.Run(ctx, nil, env.Bin("opencode"), args...)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("opencode api %s %s: unexpected response: %s", method, path, oneLine(string(raw), 200))
	}
	return nil
}

func (p openCodeProvider) Discover(ctx context.Context, env *Env) ([]Session, error) {
	procs, err := env.Procs(ctx)
	if err != nil {
		return nil, err
	}
	serviceUp := false
	var clients []Process
	for _, proc := range procs {
		if !strings.HasPrefix(commandBase(proc.Command), "opencode") {
			continue
		}
		fields := strings.Fields(proc.Command)
		sub := ""
		if len(fields) > 1 {
			sub = fields[1]
		}
		switch sub {
		case "serve":
			serviceUp = true
		case "api", "service", "upgrade", "update", "auth", "models", "mcp", "plugin", "stats", "debug", "session", "acp":
		default:
			clients = append(clients, proc)
		}
	}
	if !serviceUp {
		return nil, nil // never start the background service just to look
	}
	if _, err := exec.LookPath(env.Bin("opencode")); err != nil && env.Bin("opencode") == "opencode" {
		return nil, nil
	}
	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := p.api(ctx, env, &active, "GET", "/api/session/active", nil); err != nil {
		return nil, err
	}
	var list struct {
		Data []openCodeSession `json:"data"`
	}
	if err := p.api(ctx, env, &list, "GET", "/api/session?limit=200", nil); err != nil {
		return nil, err
	}
	clientStart := map[string]time.Time{} // directory -> earliest client start
	if len(clients) > 0 {
		pids := make([]int, len(clients))
		for i, c := range clients {
			pids[i] = c.PID
		}
		cwds, _ := env.Cwds(ctx, pids)
		for _, c := range clients {
			dir := canonicalDir(cwds[c.PID])
			if dir == "" {
				continue
			}
			if t, ok := clientStart[dir]; !ok || c.Start.Before(t) {
				clientStart[dir] = c.Start
			}
		}
	}
	isLive := func(s openCodeSession) bool {
		if _, ok := active.Data[s.ID]; ok {
			return true
		}
		start, ok := clientStart[canonicalDir(s.Location.Directory)]
		if !ok {
			return false
		}
		seen := millis(maxFloat(s.Time.Viewed, s.Time.Updated))
		return !seen.Before(start.Add(-5 * time.Second))
	}
	var sessions []Session
	for _, oc := range list.Data {
		if oc.ParentID != "" || !isLive(oc) {
			continue
		}
		root := p.session(oc, active.Data)
		sessions = append(sessions, root)
		var children struct {
			Data []openCodeSession `json:"data"`
		}
		if err := p.api(ctx, env, &children, "GET", "/api/session?limit=200&parentID="+url.QueryEscape(oc.ID), nil); err != nil {
			continue
		}
		for _, child := range children.Data {
			if child.ParentID != oc.ID {
				continue
			}
			_, running := active.Data[child.ID]
			if !running && millis(child.Time.Updated).Before(millis(oc.Time.Viewed).Add(-time.Hour)) {
				continue
			}
			s := p.session(child, active.Data)
			s.Subagent = true
			s.ParentNativeID = oc.ID
			s.ParentID = root.ID
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}

func (openCodeProvider) session(oc openCodeSession, active map[string]json.RawMessage) Session {
	status := StatusIdle
	if _, ok := active[oc.ID]; ok {
		status = StatusWorking
	}
	return Session{
		ID:        MakeID(OpenCode, oc.ID),
		Agent:     OpenCode,
		NativeID:  oc.ID,
		Title:     oc.Title,
		Cwd:       oc.Location.Directory,
		Status:    status,
		StartedAt: millis(oc.Time.Created),
		UpdatedAt: millis(oc.Time.Updated),
		Delivery:  "opencode api prompt",
	}
}

func (p openCodeProvider) Transcript(ctx context.Context, env *Env, s Session) ([]Message, error) {
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := p.api(ctx, env, &resp, "GET", "/api/session/"+url.PathEscape(s.NativeID)+"/message?limit=200&order=desc", nil); err != nil {
		return nil, err
	}
	rows := resp.Data
	sort.SliceStable(rows, func(i, j int) bool {
		return num(obj(rows[i], "time"), "created") < num(obj(rows[j], "time"), "created")
	})
	var msgs []Message
	for _, row := range rows {
		t := millis(num(obj(row, "time"), "created"))
		switch str(row, "type") {
		case "user":
			msgs = appendMessage(msgs, "user", str(row, "text"), t)
		case "assistant":
			var parts []string
			if blocks, ok := row["content"].([]any); ok {
				for _, item := range blocks {
					b, _ := item.(map[string]any)
					switch typ := str(b, "type"); {
					case typ == "text":
						parts = append(parts, str(b, "text"))
					case strings.Contains(typ, "tool"):
						parts = append(parts, "[tool: "+firstNonEmpty(str(b, "name"), str(b, "tool"), typ)+"]")
					}
				}
			}
			msgs = appendMessage(msgs, "assistant", strings.Join(parts, "\n"), t)
		}
	}
	return msgs, nil
}

func (p openCodeProvider) Send(ctx context.Context, env *Env, s Session, text string, _ SendOptions) error {
	return p.api(ctx, env, nil, "POST", "/api/session/"+url.PathEscape(s.NativeID)+"/prompt", map[string]any{"text": text})
}

func canonicalDir(dir string) string {
	if dir == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
