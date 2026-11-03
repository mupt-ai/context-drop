package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// codexProvider treats a Codex thread as live while a Codex process holds its
// writer lock (~/.codex/thread-writer-locks/<thread>.lock) and delivers
// messages with `codex queue`, which the running thread picks up.
type codexProvider struct{}

func (codexProvider) Kind() string { return Codex }

func (e *Env) codexHome() string {
	if v := e.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	return e.path(".codex")
}

func (codexProvider) Discover(ctx context.Context, env *Env) ([]Session, error) {
	home := env.codexHome()
	locks, _ := filepath.Glob(filepath.Join(home, "thread-writer-locks", "*.lock"))
	if len(locks) == 0 {
		return nil, nil
	}
	held, err := env.OpenFiles(ctx, locks)
	if err != nil {
		return nil, err
	}
	names := codexThreadNames(home)
	type live struct {
		id  string
		pid int
	}
	var threads []live
	for _, lock := range locks {
		pids := held[lock]
		if len(pids) == 0 {
			// lsof may report the resolved path.
			if resolved, err := filepath.EvalSymlinks(lock); err == nil {
				pids = held[resolved]
			}
		}
		if len(pids) == 0 {
			continue
		}
		sort.Ints(pids)
		threads = append(threads, live{strings.TrimSuffix(filepath.Base(lock), ".lock"), pids[0]})
	}
	procs, _ := env.Procs(ctx)
	var candidates []Session
	for _, th := range threads {
		rollout := codexRolloutPath(home, th.id)
		s := Session{Agent: Codex, NativeID: th.id, PID: th.pid, Transcript: rollout, Delivery: "codex queue"}
		if proc, ok := procs[th.pid]; ok {
			s.StartedAt = proc.Start
			s.TTY = proc.TTY
		}
		s.Name = names[th.id]
		if rollout != "" {
			if meta, err := readFirstJSONLine(rollout); err == nil {
				payload := obj(meta, "payload")
				s.Cwd = str(payload, "cwd")
				if created := parseTime(str(payload, "timestamp")); !created.IsZero() {
					s.StartedAt = created // when the thread began, not when its process did
				}
				if parent := codexParent(payload); parent != "" {
					s.Subagent = true
					s.ParentNativeID = parent
					s.Name = firstNonEmpty(str(payload, "agent_nickname"), s.Name)
					s.Title = str(payload, "agent_role")
				}
			}
			s.Status, s.UpdatedAt = codexStatus(rollout)
		} else {
			s.Status = StatusIdle // Codex writes the rollout with the first message
		}
		s.ID = MakeID(Codex, s.NativeID)
		candidates = append(candidates, s)
	}
	var noCwd []int
	for _, s := range candidates {
		if s.Cwd == "" && !strings.Contains(procs[s.PID].Command, "--managed-daemon") {
			noCwd = append(noCwd, s.PID)
		}
	}
	if len(noCwd) > 0 {
		cwds, _ := env.Cwds(ctx, noCwd)
		for i := range candidates {
			if candidates[i].Cwd == "" {
				candidates[i].Cwd = cwds[candidates[i].PID]
			}
		}
	}
	return codexOpenThreads(ctx, env, procs, candidates), nil
}

// codexOpenThreads drops threads that are only loaded in the shared app-server
// daemon. The daemon keeps a thread's writer lock after its terminal client
// exits, so such a thread counts as open only while a Codex client runs in the
// thread's directory and the thread has been active since that client started.
// Threads held by a dedicated process (codex exec, a private app-server) are
// open for as long as that process runs.
func codexOpenThreads(ctx context.Context, env *Env, procs map[int]Process, candidates []Session) []Session {
	var clients []Process
	for _, p := range procs {
		if commandBase(p.Command) == "codex" && p.TTY != "" && !strings.Contains(p.Command, " app-server") {
			clients = append(clients, p)
		}
	}
	clientStart := map[string]time.Time{}
	if len(clients) > 0 {
		pids := make([]int, len(clients))
		for i, c := range clients {
			pids[i] = c.PID
		}
		cwds, _ := env.Cwds(ctx, pids)
		for _, c := range clients {
			dir := canonicalDir(cwds[c.PID])
			if t, ok := clientStart[dir]; dir != "" && (!ok || c.Start.Before(t)) {
				clientStart[dir] = c.Start
			}
		}
	}
	open := map[string]bool{}
	for _, s := range candidates {
		holder := procs[s.PID]
		if !strings.Contains(holder.Command, "--managed-daemon") {
			open[s.NativeID] = true
			continue
		}
		start, ok := clientStart[canonicalDir(s.Cwd)]
		if ok && s.Cwd != "" && !s.UpdatedAt.Before(start.Add(-5*time.Second)) {
			open[s.NativeID] = true
		}
	}
	var sessions []Session
	for _, s := range candidates {
		if s.Subagent {
			s.ParentID = MakeID(Codex, s.ParentNativeID)
		}
		if open[s.NativeID] || (s.Subagent && open[s.ParentNativeID]) {
			sessions = append(sessions, s)
		}
	}
	return sessions
}

func codexParent(payload map[string]any) string {
	if p := str(payload, "parent_thread_id"); p != "" {
		return p
	}
	return str(payload, "source", "subagent", "thread_spawn", "parent_thread_id")
}

func codexRolloutPath(home, id string) string {
	for _, pattern := range []string{
		filepath.Join(home, "sessions", "*", "*", "*", "rollout-*"+id+".jsonl"),
		filepath.Join(home, "sessions", "rollout-*"+id+".jsonl"),
	} {
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
	}
	return ""
}

// codexThreadNames reads the thread names Codex records in session_index.jsonl.
func codexThreadNames(home string) map[string]string {
	names := map[string]string{}
	f, err := os.Open(filepath.Join(home, "session_index.jsonl"))
	if err != nil {
		return names
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var row struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.ID != "" && row.Name != "" {
			names[row.ID] = row.Name
		}
	}
	return names
}

func codexStatus(rollout string) (string, time.Time) {
	rows, err := readJSONLTail(rollout, 2<<20)
	updated := modTime(rollout)
	if err != nil {
		return StatusUnknown, updated
	}
	status := StatusIdle
	for _, row := range rows {
		if str(row, "type") != "event_msg" {
			continue
		}
		switch str(row, "payload", "type") {
		case "task_started", "turn_started":
			status = StatusWorking
		case "task_complete", "turn_complete", "turn_aborted", "task_aborted":
			status = StatusIdle
		case "exec_approval_request", "apply_patch_approval_request", "request_user_input":
			status = StatusBlocked
		}
	}
	return status, updated
}

func (codexProvider) Transcript(_ context.Context, _ *Env, s Session) ([]Message, error) {
	if s.Transcript == "" {
		return nil, nil // nothing has been written yet
	}
	rows, err := readJSONLTail(s.Transcript, tailBytes)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	for _, row := range rows {
		if str(row, "type") != "response_item" {
			continue
		}
		payload := obj(row, "payload")
		t := parseTime(str(row, "timestamp"))
		switch str(payload, "type") {
		case "message":
			role := str(payload, "role")
			text := blockText(payload["content"], "input_text", "output_text", "text")
			if role == "user" && codexInjected(text) {
				continue
			}
			if role == "user" || role == "assistant" {
				msgs = appendMessage(msgs, role, text, t)
			}
		case "function_call", "custom_tool_call":
			msgs = appendMessage(msgs, "assistant", "[tool: "+str(payload, "name")+"]", t)
		}
	}
	return msgs, nil
}

// codexInjected reports context Codex adds as user messages.
func codexInjected(text string) bool {
	t := strings.TrimSpace(text)
	for _, prefix := range []string{"# AGENTS.md", "<environment_context>", "<user_instructions>", "<permissions", "<INSTRUCTIONS>", "<turn_aborted>", "<codex_internal_context"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func (codexProvider) Send(ctx context.Context, env *Env, s Session, text string, _ SendOptions) error {
	args := []string{"queue", "--thread", s.NativeID, "--message", text}
	var extraEnv []string
	if procs, err := env.Procs(ctx); err == nil {
		if remote, tokenFile := codexRemote(procs[s.PID].Command); remote != "" {
			// The thread lives in a private app-server rather than the shared daemon.
			args = append(args, "--remote", remote)
			if tokenFile != "" {
				token, err := os.ReadFile(tokenFile)
				if err != nil {
					return fmt.Errorf("read Codex app-server token: %w", err)
				}
				args = append(args, "--remote-auth-token-env", codexTokenEnv)
				extraEnv = append(extraEnv, codexTokenEnv+"="+strings.TrimSpace(string(token)))
			}
		}
	}
	_, err := env.runEnv(ctx, extraEnv, nil, env.Bin("codex"), args...)
	return err
}

const codexTokenEnv = "CONTEXT_DROP_CODEX_REMOTE_TOKEN"

// codexRemote extracts the websocket address and token file of a private
// `codex app-server --listen ws://...` process.
func codexRemote(command string) (remote, tokenFile string) {
	if strings.Contains(command, "--managed-daemon") || !strings.Contains(command, "app-server") {
		return "", ""
	}
	f := strings.Fields(command)
	for i := 0; i < len(f)-1; i++ {
		switch f[i] {
		case "--listen":
			if strings.HasPrefix(f[i+1], "ws://") || strings.HasPrefix(f[i+1], "wss://") || strings.HasPrefix(f[i+1], "unix://") {
				remote = f[i+1]
			}
		case "--ws-token-file":
			tokenFile = f[i+1]
		}
	}
	return remote, tokenFile
}
