package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// museProvider finds Muse sessions whose .session.lock names a running Muse
// process. Messages go into the session's tmux pane when there is one, and
// otherwise through `muse session-message send`.
type museProvider struct{}

func (museProvider) Kind() string { return Muse }

const museReminderPrefix = "You are a reminder observer"

func (museProvider) Discover(ctx context.Context, env *Env) ([]Session, error) {
	root := env.xdgData("muse", "sessions")
	locks, _ := filepath.Glob(filepath.Join(root, "*", "*", "*", "*", ".session.lock"))
	sort.Strings(locks)
	names := museRuntimeNames(env)
	var sessions []Session
	for _, lock := range locks {
		dir := filepath.Dir(lock)
		proc, ok := museLockOwner(ctx, env, lock)
		if !ok {
			continue
		}
		s := museSession(dir, filepath.Base(dir))
		s.PID = proc.PID
		s.TTY = proc.TTY
		s.StartedAt = proc.Start
		if name := names[s.NativeID]; name != "" {
			s.Name = name
		}
		s.tmuxPane = env.tmuxPane(ctx, proc.TTY)
		if s.tmuxPane != "" {
			s.Delivery = "tmux pane " + s.tmuxPane
		} else {
			s.Delivery = "muse session-message"
		}
		s.ID = MakeID(Muse, s.NativeID)
		sessions = append(sessions, s)
		subLocks, _ := filepath.Glob(filepath.Join(dir, "subagent", "*", ".session.lock"))
		sort.Strings(subLocks)
		for _, subLock := range subLocks {
			if _, ok := museLockOwner(ctx, env, subLock); !ok {
				continue
			}
			subDir := filepath.Dir(subLock)
			sub := museSession(subDir, subagentNativeID(s.NativeID, filepath.Base(subDir)))
			if strings.HasPrefix(sub.Title, museReminderPrefix) {
				continue // internal reminder observers are not addressable agents
			}
			sub.Subagent = true
			sub.ParentNativeID = s.NativeID
			sub.ParentID = s.ID
			sub.PID = s.PID
			sub.Cwd = firstNonEmpty(sub.Cwd, s.Cwd)
			sub.Name = oneLine(sub.Title, 60)
			sub.Delivery = "relay through parent"
			sub.ID = MakeID(Muse, sub.NativeID)
			sessions = append(sessions, sub)
		}
	}
	return sessions, nil
}

// museLockOwner returns the live Muse process named by a session lock file
// ("pid=N\nhost=H"); Muse empties the lock when the session closes.
func museLockOwner(ctx context.Context, env *Env, lock string) (Process, bool) {
	data, err := os.ReadFile(lock)
	if err != nil || len(data) == 0 {
		return Process{}, false
	}
	pid, host := 0, ""
	for _, line := range strings.Split(string(data), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "pid":
			pid, _ = strconv.Atoi(value)
		case "host":
			host = value
		}
	}
	if host != "" && env.Hostname != "" && host != env.Hostname {
		return Process{}, false
	}
	proc, ok := env.Alive(ctx, pid)
	if !ok || !strings.Contains(strings.ToLower(proc.Command), "muse") {
		return Process{}, false
	}
	return proc, true
}

func museRuntimeNames(env *Env) map[string]string {
	names := map[string]string{}
	files, _ := filepath.Glob(env.xdgData("muse", "runtime", "muse", "sessions", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var reg struct {
			SessionID   string  `json:"session_id"`
			SessionName *string `json:"session_name"`
		}
		if json.Unmarshal(data, &reg) == nil && reg.SessionID != "" && reg.SessionName != nil {
			names[reg.SessionID] = *reg.SessionName
		}
	}
	return names
}

// museSession reads identity and status from a Muse session.jsonl.
func museSession(dir, nativeID string) Session {
	log := filepath.Join(dir, "session.jsonl")
	s := Session{Agent: Muse, NativeID: nativeID, Transcript: log, Status: StatusUnknown, UpdatedAt: modTime(log)}
	rows, err := readJSONLTail(log, tailBytes)
	if err != nil {
		return s
	}
	for _, row := range rows {
		payload := obj(row, "payload")
		switch str(row, "payload_type") {
		case "runtime.session.metadata":
			if ws := str(payload, "record", "workspace_root"); ws != "" {
				s.Cwd = ws
			}
		case "session.name.changed":
			if name := str(payload, "new_name"); name != "" {
				s.Name = name
			}
		case "runtime.session":
			if str(payload, "kind") != "run" {
				continue
			}
			switch str(payload, "event", "kind") {
			case "started":
				s.Status = StatusWorking
				if s.Title == "" {
					s.Title = str(payload, "event", "prompt")
				}
			case "terminal":
				s.Status = StatusIdle
			}
		}
	}
	if s.Status == StatusUnknown && len(rows) > 0 {
		s.Status = StatusIdle
	}
	s.Title = oneLine(s.Title, 200)
	return s
}

func (museProvider) Transcript(_ context.Context, _ *Env, s Session) ([]Message, error) {
	rows, err := readJSONLTail(s.Transcript, tailBytes)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	for _, row := range rows {
		if str(row, "payload_type") != "runtime.session" {
			continue
		}
		payload := obj(row, "payload")
		if str(payload, "kind") != "run" {
			continue
		}
		event := obj(payload, "event")
		t := micros(num(row, "recorded_at"))
		switch str(event, "kind") {
		case "started":
			msgs = appendMessage(msgs, "user", str(event, "prompt"), t)
		case "assistant_message_committed":
			msgs = appendMessage(msgs, "assistant", str(event, "text"), t)
		case "assistant_tool_calls_committed":
			calls, _ := event["tool_calls"].([]any)
			var names []string
			for _, c := range calls {
				if m, ok := c.(map[string]any); ok {
					names = append(names, "[tool: "+str(m, "name")+"]")
				}
			}
			msgs = appendMessage(msgs, "assistant", strings.Join(names, " "), t)
		}
	}
	return msgs, nil
}

func (museProvider) Send(ctx context.Context, env *Env, s Session, text string, opts SendOptions) error {
	if s.Subagent {
		return fmt.Errorf("muse subagents are reached through their parent; send to %s instead", s.ParentID)
	}
	if s.tmuxPane != "" {
		return tmuxSubmit(ctx, env, s.tmuxPane, text)
	}
	out, err := env.runEnv(ctx, []string{"MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=1"}, []byte(text),
		env.Bin("muse"), "session-message", "send", "--target", s.NativeID, "--json")
	var resp struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(firstLine(out), &resp)
	switch resp.Status {
	case "accepted", "delivered", "duplicate", "pending_router_delivery", "external_transport_accepted":
		return nil
	}
	reason := firstNonEmpty(resp.ErrorCode, resp.Status)
	if reason == "" && err != nil {
		reason = err.Error()
	}
	return fmt.Errorf("muse refused the message (%s); run the Muse session inside tmux so context-drop can type into it", reason)
}

func firstLine(b []byte) []byte {
	if i := strings.IndexByte(string(b), '\n'); i >= 0 {
		return b[:i]
	}
	return b
}
