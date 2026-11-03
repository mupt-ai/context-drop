package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Multiplexers Context Drop can type into and launch agents in.
const (
	MuxTmux  = "tmux"
	MuxHerdr = "herdr"
	MuxRex   = "rex"
)

// Multiplexers lists the supported multiplexers.
var Multiplexers = []string{MuxTmux, MuxHerdr, MuxRex}

// paneRef is a terminal pane an agent runs in, when one can be typed into.
type paneRef struct {
	backend string // tmux, herdr, or rex
	id      string
}

func (p paneRef) describe() string {
	if p.backend == "" {
		return ""
	}
	return p.backend + " pane " + p.id
}

// resolvePane finds the pane that runs pid: tmux by terminal, then herdr and
// rex by the process tree, so a wrapper such as `dari pi` still matches.
func resolvePane(ctx context.Context, env *Env, pid int, tty string) paneRef {
	if id := env.tmuxPane(ctx, tty); id != "" {
		return paneRef{MuxTmux, id}
	}
	if id := ancestorPane(ctx, env, pid, env.herdrPanes(ctx)); id != "" {
		return paneRef{MuxHerdr, id}
	}
	if id := ancestorPane(ctx, env, pid, env.rexPanes(ctx)); id != "" {
		return paneRef{MuxRex, id}
	}
	return paneRef{}
}

// ancestorPane walks pid and its parents until one is a process known to a pane.
func ancestorPane(ctx context.Context, env *Env, pid int, panes map[int]string) string {
	if len(panes) == 0 {
		return ""
	}
	procs, _ := env.Procs(ctx)
	for seen := 0; pid > 1 && seen < 64; seen++ {
		if id, ok := panes[pid]; ok {
			return id
		}
		p, ok := procs[pid]
		if !ok {
			return ""
		}
		pid = p.PPID
	}
	return ""
}

// submitToPane types text into the pane and presses Enter.
func submitToPane(ctx context.Context, env *Env, p paneRef, text string) error {
	switch p.backend {
	case MuxTmux:
		return tmuxSubmit(ctx, env, p.id, text)
	case MuxHerdr:
		return herdrSubmit(ctx, env, p.id, text)
	case MuxRex:
		return rexSubmit(ctx, env, p.id, text)
	}
	return fmt.Errorf("no pane to type into")
}

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

// rexPanes maps the processes of every rex block (its shell, foreground
// process, and their ancestors) to the block ID.
func (e *Env) rexPanes(ctx context.Context) map[int]string {
	e.rexOnce.Do(func() {
		e.rex = map[int]string{}
		out, err := e.Run(ctx, nil, e.rexBin(), "block", "ls", "--json")
		if err != nil {
			return
		}
		var list struct {
			Blocks []struct {
				BlockID string `json:"block_id"`
			} `json:"blocks"`
		}
		if json.Unmarshal(out, &list) != nil {
			return
		}
		for _, b := range list.Blocks {
			raw, err := e.Run(ctx, nil, e.rexBin(), "block", "call", "process", "-b", b.BlockID)
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
			for _, pid := range []int{info.Child.PID, info.Foreground.PID} {
				if pid > 0 {
					e.rex[pid] = b.BlockID
				}
			}
		}
	})
	return e.rex
}

// rexSubmit types text into a rex block and presses Enter.
func rexSubmit(ctx context.Context, env *Env, block, text string) error {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\r\n", "\n")), " ")
	if _, err := env.Run(ctx, nil, env.rexBin(), "send", "-b", block, text); err != nil {
		return err
	}
	_, err := env.Run(ctx, nil, env.rexBin(), "send-key", "-b", block, "enter")
	return err
}
