package agents

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// launchRecord remembers a pane `start` opened for a session, so `stop` can
// close panes Context Drop created and leave panes the user opened alone.
// The session's process is part of the key: pane IDs (tmux "%3") are reused
// after a restart, and a resumed session keeps its ID but not its process.
type launchRecord struct {
	Session string    `json:"session"`
	PID     int       `json:"pid,omitempty"`
	Mux     string    `json:"mux"`
	Pane    string    `json:"pane"`
	Scope   string    `json:"scope,omitempty"`
	At      time.Time `json:"at"`
}

// maxLaunchRecords bounds launched.json; old entries are dropped first.
const maxLaunchRecords = 500

func (e *Env) launchesPath() string { return filepath.Join(e.ContextDropHome(), "launched.json") }

// recordLaunch notes that the session runs in a pane `start` opened.
func (e *Env) recordLaunch(s Session, at Location) error {
	var records []launchRecord
	return updateJSON(e.launchesPath(), &records, func() (bool, error) {
		records = append(records, launchRecord{Session: s.ID, PID: s.PID, Mux: at.Mux, Pane: at.Pane, Scope: at.scope, At: time.Now()})
		if len(records) > maxLaunchRecords {
			records = records[len(records)-maxLaunchRecords:]
		}
		return true, nil
	})
}

// takeLaunch finds and removes the record of the pane `start` opened for
// this run of the session.
// A session that changed ID since (a Pi session without the extension takes
// its file's ID after its first message) matches by its process, as long as
// it still runs in the recorded pane.
func (e *Env) takeLaunch(ctx context.Context, s Session) (Location, bool) {
	var records []launchRecord
	var found Location
	ok := false
	here, located := Location{}, false
	if s.PID > 0 {
		here, located = locate(ctx, e, s.PID, s.TTY)
	}
	_ = updateJSON(e.launchesPath(), &records, func() (bool, error) {
		for i, r := range records {
			renamed := r.Session != s.ID && r.PID > 0 && located && here.Mux == r.Mux && here.Pane == r.Pane
			if r.PID == s.PID && (r.Session == s.ID || renamed) {
				found, ok = Location{Mux: r.Mux, Pane: r.Pane, scope: r.Scope}, true
				records = append(records[:i], records[i+1:]...)
				return true, nil
			}
		}
		return false, nil
	})
	return found, ok
}

// Stop ends a session. A session in a pane that `start` opened for it has
// that pane closed, found from the launch record even when the session's
// process does not lead to it (OpenCode, a Codex app-server thread). Any
// other session gets SIGTERM, so a pane the user opened keeps its shell.
// Processes that serve several sessions (a Codex app-server, `opencode
// serve`) are never signalled. It returns what it did.
func Stop(ctx context.Context, env *Env, s Session) (string, error) {
	if s.Subagent {
		return "", fmt.Errorf("%s is a subagent; stop its parent %s instead", s.ID, s.ParentID)
	}
	if at, ok := env.takeLaunch(ctx, s); ok {
		m, err := muxFor(at.Mux)
		if err != nil {
			return "", err
		}
		if err := m.close(ctx, env, at); err != nil {
			_ = env.recordLaunch(s, at) // keep it, so a retry can close the pane
			return "", err
		}
		return "closed " + at.describe(), nil
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
