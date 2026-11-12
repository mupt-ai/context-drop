package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// launchRecord remembers a pane `start` opened for a session, so `stop` can
// close panes Context Drop created and leave panes the user opened alone.
type launchRecord struct {
	Session string    `json:"session"`
	Mux     string    `json:"mux"`
	Pane    string    `json:"pane"`
	At      time.Time `json:"at"`
}

// maxLaunchRecords bounds launched.json; old entries are dropped first.
const maxLaunchRecords = 500

func (e *Env) launchesPath() string { return filepath.Join(e.ContextDropHome(), "launched.json") }

func (e *Env) launches() []launchRecord {
	var records []launchRecord
	if data, err := os.ReadFile(e.launchesPath()); err == nil {
		_ = json.Unmarshal(data, &records)
	}
	return records
}

// recordLaunch notes that the session runs in a pane `start` opened.
func (e *Env) recordLaunch(s Session, at Location) error {
	records := append(e.launches(), launchRecord{Session: s.ID, Mux: at.Mux, Pane: at.Pane, At: time.Now()})
	if len(records) > maxLaunchRecords {
		records = records[len(records)-maxLaunchRecords:]
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.ContextDropHome(), 0o700); err != nil {
		return err
	}
	tmp := e.launchesPath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.launchesPath())
}

// launchedHere reports whether `start` opened the pane for this session.
func (e *Env) launchedHere(s Session, at Location) bool {
	for _, r := range e.launches() {
		if r.Session == s.ID && r.Mux == at.Mux && r.Pane == at.Pane {
			return true
		}
	}
	return false
}

// Stop ends a session. A session in a pane that `start` opened for it has
// that pane closed. Any other session gets SIGTERM, so a pane the user opened
// keeps its shell. Processes that serve several sessions (a Codex app-server,
// `opencode serve`) are never signalled. It returns what it did.
func Stop(ctx context.Context, env *Env, s Session) (string, error) {
	if s.Subagent {
		return "", fmt.Errorf("%s is a subagent; stop its parent %s instead", s.ID, s.ParentID)
	}
	if s.Location == nil && s.PID > 0 {
		if loc, ok := locate(ctx, env, s.PID, s.TTY); ok {
			s.Location = &loc
		}
	}
	if s.Location != nil && env.launchedHere(s, *s.Location) {
		m, err := muxFor(s.Location.Mux)
		if err != nil {
			return "", err
		}
		if err := m.close(ctx, env, *s.Location); err != nil {
			return "", err
		}
		return "closed " + s.Location.describe(), nil
	}
	proc, ok := env.Alive(ctx, s.PID)
	if !ok {
		return "", fmt.Errorf("%s has no running process to stop", s.ID)
	}
	if sharedServer(proc.Command) {
		return "", fmt.Errorf("%s is served by a shared process (pid %d, %s) that other sessions use; close its terminal instead",
			s.ID, proc.PID, oneLine(proc.Command, 60))
	}
	if env.Signal == nil {
		return "", fmt.Errorf("cannot signal processes here")
	}
	if err := env.Signal(proc.PID, syscall.SIGTERM); err != nil {
		return "", fmt.Errorf("signal %s (pid %d): %w", s.ID, proc.PID, err)
	}
	return fmt.Sprintf("sent SIGTERM to %s (pid %d)", commandBase(proc.Command), proc.PID), nil
}

func sharedServer(command string) bool {
	f := strings.Fields(command)
	for i, w := range f {
		if i > 0 && (w == "app-server" || w == "serve") {
			return true
		}
	}
	return false
}
