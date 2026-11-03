package agents

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed pi_extension.ts
var PiExtension []byte

// piProvider finds Pi sessions through the registry the Context Drop Pi
// extension publishes, and falls back to matching running `pi` processes to
// their newest session file. Messages go through the extension's socket, or
// into the session's tmux pane when the extension is not loaded.
type piProvider struct{}

func (piProvider) Kind() string { return Pi }

type piRegistry struct {
	PID         int    `json:"pid"`
	SessionID   string `json:"sessionId"`
	SessionFile string `json:"sessionFile"`
	Cwd         string `json:"cwd"`
	Name        string `json:"name"`
	Socket      string `json:"socket"`
	Status      string `json:"status"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// PiExtensionPath is where `context-drop setup pi` installs the extension.
func (e *Env) PiExtensionPath() string {
	dir := e.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		dir = e.path(".pi", "agent")
	}
	return filepath.Join(dir, "extensions", "context-drop.ts")
}

func (e *Env) piSessionsDir() string {
	dir := e.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		dir = e.path(".pi", "agent")
	}
	return filepath.Join(dir, "sessions")
}

func isPiProcess(command string) bool {
	f := strings.Fields(command)
	if len(f) == 0 {
		return false
	}
	if filepath.Base(f[0]) == "pi" {
		return true
	}
	// node /path/to/bin/pi or the pi-coding-agent cli entry
	if strings.HasPrefix(filepath.Base(f[0]), "node") || strings.HasPrefix(filepath.Base(f[0]), "bun") {
		if len(f) > 1 && (filepath.Base(f[1]) == "pi" || strings.Contains(f[1], "pi-coding-agent")) {
			return true
		}
	}
	return false
}

func (piProvider) Discover(ctx context.Context, env *Env) ([]Session, error) {
	procs, err := env.Procs(ctx)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	registered := map[int]bool{}
	files, _ := filepath.Glob(filepath.Join(env.ContextDropHome(), "pi", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var reg piRegistry
		if json.Unmarshal(data, &reg) != nil || reg.SessionID == "" {
			continue
		}
		proc, ok := procs[reg.PID]
		// A crashed Pi leaves its entry behind; a reused PID must not revive it.
		if !ok || !isPiProcess(proc.Command) || proc.Start.After(millis(float64(reg.UpdatedAt)).Add(time.Minute)) {
			continue
		}
		registered[reg.PID] = true
		s := Session{
			Agent: Pi, NativeID: reg.SessionID, Name: reg.Name, Cwd: reg.Cwd, PID: reg.PID, TTY: proc.TTY,
			StartedAt: proc.Start, UpdatedAt: latest(millis(float64(reg.UpdatedAt)), modTime(reg.SessionFile)),
			Transcript: reg.SessionFile, Status: piStatus(reg.Status), socket: reg.Socket,
			Delivery: "pi extension socket",
		}
		s.Title = piTitle(reg.SessionFile)
		s.ID = MakeID(Pi, s.NativeID)
		sessions = append(sessions, s)
	}
	// Pi processes started without the extension.
	var unregistered []Process
	for _, p := range procs {
		if !registered[p.PID] && isPiProcess(p.Command) {
			unregistered = append(unregistered, p)
		}
	}
	if len(unregistered) == 0 {
		return sessions, nil
	}
	sort.Slice(unregistered, func(i, j int) bool { return unregistered[i].PID < unregistered[j].PID })
	pids := make([]int, len(unregistered))
	for i, p := range unregistered {
		pids[i] = p.PID
	}
	cwds, _ := env.Cwds(ctx, pids)
	perCwd := map[string]int{}
	for _, p := range unregistered {
		perCwd[cwds[p.PID]]++
	}
	for _, p := range unregistered {
		cwd := cwds[p.PID]
		s := Session{Agent: Pi, Cwd: cwd, PID: p.PID, TTY: p.TTY, StartedAt: p.Start, Status: StatusUnknown}
		if cwd != "" && perCwd[cwd] == 1 {
			if file := newestPiSession(env, cwd, p.Start); file != "" {
				s.Transcript = file
				if header, err := readFirstJSONLine(file); err == nil {
					s.NativeID = str(header, "id")
				}
				s.UpdatedAt = modTime(file)
				s.Title = piTitle(file)
			}
		}
		if s.NativeID == "" {
			// No session file can be attributed to this process yet.
			s.NativeID = fmt.Sprintf("pid-%d-%d", p.PID, p.Start.Unix())
		}
		s.tmuxPane = env.tmuxPane(ctx, p.TTY)
		if s.tmuxPane != "" {
			s.Delivery = "tmux pane " + s.tmuxPane
		} else {
			s.Delivery = "unavailable (run `context-drop setup pi`, then restart pi)"
		}
		s.ID = MakeID(Pi, s.NativeID)
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// newestPiSession returns the session file for cwd modified since the process started.
func newestPiSession(env *Env, cwd string, since time.Time) string {
	encoded := "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimPrefix(cwd, "/")) + "--"
	files, _ := filepath.Glob(filepath.Join(env.piSessionsDir(), encoded, "*.jsonl"))
	best, bestTime := "", time.Time{}
	for _, f := range files {
		t := modTime(f)
		if !since.IsZero() && t.Before(since) {
			continue
		}
		if t.After(bestTime) {
			best, bestTime = f, t
		}
	}
	return best
}

func piStatus(status string) string {
	switch status {
	case "working":
		return StatusWorking
	case "idle":
		return StatusIdle
	}
	return StatusUnknown
}

// piTitle returns the first user prompt of a Pi session.
func piTitle(file string) string {
	if file == "" {
		return ""
	}
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for i := 0; scanner.Scan() && i < 200; i++ {
		var row map[string]any
		if json.Unmarshal(scanner.Bytes(), &row) != nil || str(row, "type") != "message" {
			continue
		}
		msg := obj(row, "message")
		if str(msg, "role") == "user" {
			return oneLine(blockText(msg["content"], "text"), 200)
		}
	}
	return ""
}

func (piProvider) Transcript(_ context.Context, _ *Env, s Session) ([]Message, error) {
	if s.Transcript == "" {
		return nil, nil // nothing has been written yet
	}
	rows, err := readJSONLTail(s.Transcript, tailBytes)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	for _, row := range rows {
		if str(row, "type") != "message" {
			continue
		}
		msg := obj(row, "message")
		role := str(msg, "role")
		if role != "user" && role != "assistant" {
			continue
		}
		msgs = appendMessage(msgs, role, blockText(msg["content"], "text"), parseTime(str(row, "timestamp")))
	}
	return msgs, nil
}

func (piProvider) Send(ctx context.Context, env *Env, s Session, text string, _ SendOptions) error {
	if s.socket == "" {
		if s.tmuxPane != "" {
			return tmuxSubmit(ctx, env, s.tmuxPane, text)
		}
		return fmt.Errorf("pi session %s has no Context Drop extension loaded and is not in tmux; run `context-drop setup pi` and restart pi", s.ID)
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", s.socket)
	if err != nil {
		return fmt.Errorf("connect to pi session %s: %w", s.ID, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, _ := json.Marshal(map[string]string{"type": "user", "text": text})
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(reply) == 0 {
		return fmt.Errorf("pi session %s did not acknowledge the message: %w", s.ID, err)
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &resp); err != nil {
		return fmt.Errorf("pi session %s: bad reply %q", s.ID, oneLine(string(reply), 200))
	}
	if !resp.OK {
		return fmt.Errorf("pi session %s refused the message: %s", s.ID, resp.Error)
	}
	return nil
}
