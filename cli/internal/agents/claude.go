package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// claudeProvider reads Claude Code's live-session registry
// (~/.claude/sessions/<pid>.json) and delivers messages through the peer
// messaging socket each interactive session listens on.
type claudeProvider struct{}

func (claudeProvider) Kind() string { return Claude }

type claudeRegistry struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	StartedAt       int64  `json:"startedAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	MessagingSocket string `json:"messagingSocketPath"`
	Tmux            string `json:"tmux"`
}

type claudeSubagentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Finished    bool   `json:"finished"`
}

func (claudeProvider) Discover(ctx context.Context, env *Env) ([]Session, error) {
	dir := env.path(".claude", "sessions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var reg claudeRegistry
		if json.Unmarshal(data, &reg) != nil || reg.SessionID == "" {
			continue
		}
		proc, ok := env.Alive(ctx, reg.PID)
		if !ok || !strings.Contains(strings.ToLower(proc.Command), "claude") {
			continue
		}
		transcript := claudeTranscriptPath(env, reg.Cwd, reg.SessionID)
		s := Session{
			Agent:      Claude,
			NativeID:   reg.SessionID,
			Name:       reg.Name,
			Cwd:        reg.Cwd,
			PID:        reg.PID,
			TTY:        proc.TTY,
			Status:     claudeStatus(reg.Status),
			StartedAt:  millis(float64(reg.StartedAt)),
			UpdatedAt:  latest(millis(float64(reg.UpdatedAt)), modTime(transcript)),
			Transcript: transcript,
			socket:     reg.MessagingSocket,
		}
		if s.socket != "" {
			s.Delivery = "claude peer socket"
		} else {
			s.Delivery = "unavailable (no messaging socket)"
		}
		s.ID = MakeID(Claude, s.NativeID)
		sessions = append(sessions, s)
		sessions = append(sessions, claudeSubagents(env, s)...)
	}
	return sessions, nil
}

func claudeSubagents(env *Env, parent Session) []Session {
	if parent.Transcript == "" {
		return nil
	}
	dir := filepath.Join(strings.TrimSuffix(parent.Transcript, ".jsonl"), "subagents")
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	sort.Strings(metas)
	var subs []Session
	for _, metaPath := range metas {
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta claudeSubagentMeta
		if json.Unmarshal(data, &meta) != nil {
			continue
		}
		transcript := strings.TrimSuffix(metaPath, ".meta.json") + ".jsonl"
		updated := modTime(transcript)
		if !parent.StartedAt.IsZero() && updated.Before(parent.StartedAt) {
			continue // left over from an earlier run of the parent
		}
		// Finished subagents stay addressable: SendMessage resumes them.
		status := StatusWorking
		if meta.Finished {
			status = StatusIdle
		}
		agentID := strings.TrimPrefix(filepath.Base(strings.TrimSuffix(metaPath, ".meta.json")), "agent-")
		native := subagentNativeID(parent.NativeID, agentID)
		subs = append(subs, Session{
			ID:             MakeID(Claude, native),
			Agent:          Claude,
			NativeID:       native,
			ParentID:       parent.ID,
			ParentNativeID: parent.NativeID,
			Subagent:       true,
			Name:           meta.Description,
			Title:          meta.AgentType,
			Cwd:            parent.Cwd,
			PID:            parent.PID,
			Status:         status,
			UpdatedAt:      updated,
			Transcript:     transcript,
			Delivery:       "relay through parent (SendMessage)",
		})
	}
	return subs
}

func claudeTranscriptPath(env *Env, cwd, sessionID string) string {
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	direct := env.path(".claude", "projects", slug, sessionID+".jsonl")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	matches, _ := filepath.Glob(env.path(".claude", "projects", "*", sessionID+".jsonl"))
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches[0]
	}
	return ""
}

func claudeStatus(status string) string {
	s := strings.ToLower(status)
	switch {
	case s == "busy" || s == "working" || s == "running" || s == "thinking" || s == "shell" || s == "tool":
		return StatusWorking
	case s == "idle" || s == "done" || s == "ready":
		return StatusIdle
	case strings.Contains(s, "wait") || strings.Contains(s, "block") || strings.Contains(s, "permission") || strings.Contains(s, "input"):
		return StatusBlocked
	}
	return StatusUnknown
}

func (claudeProvider) Transcript(_ context.Context, _ *Env, s Session) ([]Message, error) {
	if s.Transcript == "" {
		return nil, nil // nothing has been written yet
	}
	rows, err := readJSONLTail(s.Transcript, tailBytes)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	for _, row := range rows {
		typ := str(row, "type")
		if typ != "user" && typ != "assistant" {
			continue
		}
		peer := str(row, "origin", "kind") == "peer"
		if row["isMeta"] == true && !peer {
			continue
		}
		t := parseTime(str(row, "timestamp"))
		content := obj(row, "message")["content"]
		if typ == "user" {
			text := blockText(content, "text")
			if strings.HasPrefix(text, "<local-command") || strings.HasPrefix(text, "<command-") {
				continue
			}
			if peer {
				text = claudePeerText(text, str(row, "origin", "from"))
			}
			msgs = appendMessage(msgs, "user", text, t)
		} else {
			msgs = appendMessage(msgs, "assistant", blockText(content, "text"), t)
		}
	}
	return msgs, nil
}

func (p claudeProvider) Send(ctx context.Context, env *Env, s Session, text string, opts SendOptions) error {
	if s.socket == "" {
		return fmt.Errorf("%s has no messaging socket; it may be a non-interactive Claude Code session", s.ID)
	}
	token, err := claudePeerToken(env, s.PID, s.socket)
	if err != nil {
		return err
	}
	from := firstNonEmpty(opts.From, "context-drop")
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", s.socket)
	if err != nil {
		return fmt.Errorf("connect to Claude Code session %s: %w", s.ID, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	sessionID := s.NativeID
	frames := []any{
		map[string]any{"type": "auth", "token": token},
		map[string]any{"type": "user", "from": from, "session_id": sessionID, "message": map[string]any{"content": text}},
	}
	for _, frame := range frames {
		line, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if _, err := conn.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("send to Claude Code session %s: %w", s.ID, err)
		}
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	return nil
}

// claudePeerToken reads the inbox token Claude Code publishes next to its
// registry entry as <pid>.<sha256(socket path)>.key.
func claudePeerToken(env *Env, pid int, socket string) (string, error) {
	dir := env.path(".claude", "sessions")
	sum := sha256.Sum256([]byte(socket))
	candidates := []string{filepath.Join(dir, strconv.Itoa(pid)+"."+hex.EncodeToString(sum[:])+".key")}
	globbed, _ := filepath.Glob(filepath.Join(dir, strconv.Itoa(pid)+".*.key"))
	sort.Strings(globbed)
	candidates = append(candidates, globbed...)
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var key struct {
			PeerToken string `json:"peerToken"`
		}
		if json.Unmarshal(data, &key) == nil && key.PeerToken != "" {
			return key.PeerToken, nil
		}
	}
	return "", fmt.Errorf("no peer token for Claude Code pid %d in %s", pid, dir)
}

func latest(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// claudePeerText strips the framing Claude Code wraps around peer messages.
func claudePeerText(text, from string) string {
	if _, rest, ok := strings.Cut(text, "sent a message:\n"); ok {
		text = rest
	}
	if i := strings.Index(text, "\n\nThis came from another Claude session"); i >= 0 {
		text = text[:i]
	}
	if from != "" {
		return "[from " + from + "] " + strings.TrimSpace(text)
	}
	return strings.TrimSpace(text)
}
