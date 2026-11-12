package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
)

// rexMux drives Rex through the rex CLI. Discovery passes --autostart=false so
// that listing agents never starts a Rex server.
type rexMux struct{}

// rexBin finds the rex executable, which ships inside Rex.app and is often not on PATH.
func (e *Env) rexBin() string {
	if v := e.Getenv("CONTEXT_DROP_REX_BIN"); v != "" {
		return v
	}
	if dir := e.Getenv("REX_BIN_DIR"); dir != "" {
		return filepath.Join(dir, "rex")
	}
	if path, err := exec.LookPath("rex"); err == nil {
		return path
	}
	for _, app := range []string{"/Applications/Rex.app", "/Applications/Rex Beta.app"} {
		bin := filepath.Join(app, "Contents", "Helpers", "Rex Server.rex", "Contents", "MacOS", "rex")
		if _, err := exec.LookPath(bin); err == nil {
			return bin
		}
	}
	return "rex"
}

// rexScope adds -s SESSION when the block's session is known.
func rexScope(args []string, session string) []string {
	if session != "" {
		return append(args, "-s", session)
	}
	return args
}

func (rexMux) open(ctx context.Context, env *Env, at Placement, dir, label string, argv []string) (Location, error) {
	args := rexScope([]string{"run"}, at.Session)
	if at.Window != "" {
		args = append(args, "-w", at.Window)
	}
	if at.Pane != "" {
		args = append(args, "-b", at.Pane)
	}
	if at.Window != "" || at.Pane != "" {
		args = append(args, "--split", "right") // rex places a block in a window only as a split
	}
	args = append(append(args, "--cwd", dir, "--label", label, "--json", "--"), argv...)
	out, err := env.Run(ctx, nil, env.rexBin(), args...)
	if err != nil {
		return Location{}, err
	}
	var created struct {
		WindowID string   `json:"window_id"`
		BlockIDs []string `json:"block_ids"`
	}
	if json.Unmarshal(out, &created) != nil || len(created.BlockIDs) == 0 {
		return Location{}, fmt.Errorf("rex did not report the new block: %s", oneLine(string(out), 200))
	}
	loc := Location{Mux: MuxRex, Session: at.Session, Window: created.WindowID, Pane: created.BlockIDs[0], Label: label, scope: at.Session}
	if loc.scope == "" {
		// Rex only finds a block outside the caller's session with -s, so
		// look up where a split placed by window or block alone landed.
		if found, ok := rexFind(ctx, env, loc.Pane); ok {
			loc.Session, loc.Window, loc.scope = found.Session, found.Window, found.scope
		}
	}
	return loc, nil
}

// rexFind finds which session holds a block.
func rexFind(ctx context.Context, env *Env, block string) (Location, bool) {
	rex := env.rexBin()
	out, err := env.Run(ctx, nil, rex, "--autostart=false", "--timeout=3s", "ls", "--json")
	if err != nil {
		return Location{}, false
	}
	var sessions rexSessions
	if json.Unmarshal(out, &sessions) != nil {
		return Location{}, false
	}
	for _, s := range sessions.Sessions {
		for _, b := range rexBlocks(ctx, env, s.SessionID) {
			if b.BlockID == block {
				return Location{Mux: MuxRex, Session: firstNonEmpty(s.Label, s.SessionID), Window: b.WindowID, Pane: b.BlockID, scope: s.SessionID}, true
			}
		}
	}
	return Location{}, false
}

type rexSessions struct {
	Sessions []struct {
		SessionID string `json:"session_id"`
		Label     string `json:"label"`
	} `json:"sessions"`
}

type rexBlock struct {
	BlockID  string `json:"block_id"`
	Label    string `json:"label"`
	WindowID string `json:"window_id"`
}

func rexBlocks(ctx context.Context, env *Env, session string) []rexBlock {
	out, err := env.Run(ctx, nil, env.rexBin(), "--autostart=false", "--timeout=3s", "block", "ls", "-s", session, "--json")
	if err != nil {
		return nil
	}
	var list struct {
		Blocks []rexBlock `json:"blocks"`
	}
	_ = json.Unmarshal(out, &list)
	return list.Blocks
}

func (rexMux) locate(ctx context.Context, env *Env, pid int, _ string) (Location, bool) {
	return ancestorPane(ctx, env, pid, env.rexPanes(ctx))
}

// rexPanes maps the processes of every rex block in every session (its shell
// and foreground process) to the block.
func (e *Env) rexPanes(ctx context.Context) map[int]Location {
	e.rexOnce.Do(func() {
		e.rex = map[int]Location{}
		rex := e.rexBin()
		out, err := e.Run(ctx, nil, rex, "--autostart=false", "--timeout=3s", "ls", "--json")
		if err != nil {
			return
		}
		var sessions rexSessions
		if json.Unmarshal(out, &sessions) != nil {
			return
		}
		for _, s := range sessions.Sessions {
			for _, b := range rexBlocks(ctx, e, s.SessionID) {
				raw, err := e.Run(ctx, nil, rex, "--autostart=false", "--timeout=3s", "block", "call", "process", "-s", s.SessionID, "-b", b.BlockID)
				if err != nil {
					continue
				}
				var info struct {
					Child      struct{ PID int } `json:"child"`
					Foreground struct{ PID int } `json:"foreground"`
				}
				if json.Unmarshal(raw, &info) != nil {
					continue
				}
				loc := Location{Mux: MuxRex, Session: firstNonEmpty(s.Label, s.SessionID), Window: b.WindowID, Pane: b.BlockID, Label: b.Label,
					scope: s.SessionID, aliases: [3][]string{{s.SessionID}, nil, nil}}
				for _, pid := range []int{info.Child.PID, info.Foreground.PID} {
					if pid > 0 {
						e.rex[pid] = loc
					}
				}
			}
		}
	})
	return e.rex
}

func (rexMux) capture(ctx context.Context, env *Env, at Location, lines int) (string, error) {
	// rex prints the whole scrollback; keep the end.
	out, err := env.Run(ctx, nil, env.rexBin(), append(rexScope([]string{"capture"}, at.scope), "-b", at.Pane, "--trim")...)
	return lastLines(string(out), lines), err
}

func (m rexMux) submit(ctx context.Context, env *Env, at Location, text string) error {
	if _, err := env.Run(ctx, nil, env.rexBin(), append(rexScope([]string{"send"}, at.scope), "-b", at.Pane, singleLine(text))...); err != nil {
		return err
	}
	return m.press(ctx, env, at, "enter")
}

func (rexMux) press(ctx context.Context, env *Env, at Location, key string) error {
	_, err := env.Run(ctx, nil, env.rexBin(), append(rexScope([]string{"send-key"}, at.scope), "-b", at.Pane, key)...)
	return err
}

func (rexMux) close(ctx context.Context, env *Env, at Location) error {
	_, err := env.Run(ctx, nil, env.rexBin(), append(rexScope([]string{"block", "close"}, at.scope), at.Pane)...)
	return err
}
