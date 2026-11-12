package agents

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// tmuxMux drives tmux. Panes are found by terminal, which tmux reports for
// every pane, so an agent is located whichever process tree started it.
type tmuxMux struct{}

// tmuxDefaultSession holds the windows `start` opens when no place is named.
const tmuxDefaultSession = "context-drop"

func (tmuxMux) open(ctx context.Context, env *Env, at Placement, dir, label string, argv []string) (Location, error) {
	tmux := env.Bin("tmux")
	report := []string{"-c", dir, "-P", "-F", "#{pane_id}\t#{session_name}\t#{window_index}", "--"}
	var out []byte
	var err error
	if at.Window != "" || at.Pane != "" {
		out, err = env.Run(ctx, nil, tmux, append(append([]string{"split-window", "-h", "-t", tmuxTarget(at)}, report...), argv...)...)
	} else {
		session := firstNonEmpty(at.Session, tmuxDefaultSession)
		window := append([]string{"-n", label}, report...)
		out, err = env.Run(ctx, nil, tmux, append(append([]string{"new-window", "-t", session + ":"}, window...), argv...)...)
		if err != nil && at.Session == "" {
			// No context-drop session yet: create it with the agent as its first window.
			out, err = env.Run(ctx, nil, tmux, append(append([]string{"new-session", "-d", "-s", session}, window...), argv...)...)
		}
	}
	if err != nil {
		return Location{}, err
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\t")
	if f[0] == "" {
		return Location{}, fmt.Errorf("tmux did not report the new pane")
	}
	loc := Location{Mux: MuxTmux, Pane: f[0]}
	if len(f) == 3 {
		loc.Session, loc.Window = f[1], f[2]
	}
	return loc, nil
}

// tmuxTarget turns a placement into a tmux target: a pane ID or full target
// is used as is, otherwise "SESSION:WINDOW.PANE" with the parts given.
func tmuxTarget(at Placement) string {
	if strings.HasPrefix(at.Pane, "%") || strings.ContainsAny(at.Pane, ":.") {
		return at.Pane
	}
	if at.Session == "" && at.Pane == "" && (strings.HasPrefix(at.Window, "@") || strings.Contains(at.Window, ":")) {
		return at.Window
	}
	target := at.Session + ":" + at.Window
	if at.Pane != "" {
		target += "." + at.Pane
	}
	return target
}

func (tmuxMux) locate(ctx context.Context, env *Env, _ int, tty string) (Location, bool) {
	if tty == "" {
		return Location{}, false
	}
	loc, ok := env.tmuxPanes(ctx)[tty]
	return loc, ok
}

// tmuxPanes maps each tmux pane's terminal to the pane.
func (e *Env) tmuxPanes(ctx context.Context) map[string]Location {
	e.tmuxOnce.Do(func() {
		e.tmux = map[string]Location{}
		out, err := e.Run(ctx, nil, e.Bin("tmux"), "list-panes", "-a", "-F",
			"#{pane_id}\t#{pane_tty}\t#{session_name}\t#{window_index}\t#{window_id}\t#{window_name}\t#{pane_index}")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Split(strings.TrimSpace(line), "\t")
			if len(f) < 2 {
				continue
			}
			loc := Location{Mux: MuxTmux, Pane: f[0]}
			if len(f) == 7 {
				// The window name names the window, so it is a window alias, not the pane's label.
				loc.Session, loc.Window = f[2], f[3]
				loc.aliases = [3][]string{nil, {f[4], f[5]}, {f[6]}}
			}
			e.tmux[f[1]] = loc
		}
	})
	return e.tmux
}

func (tmuxMux) capture(ctx context.Context, env *Env, at Location, lines int) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", at.Pane}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	out, err := env.Run(ctx, nil, env.Bin("tmux"), args...)
	return lastLines(string(out), lines), err
}

// submit types text as a literal paste and presses Enter.
func (m tmuxMux) submit(ctx context.Context, env *Env, at Location, text string) error {
	if at.Pane == "" {
		return fmt.Errorf("session is not running in a tmux pane")
	}
	if _, err := env.Run(ctx, nil, env.Bin("tmux"), "send-keys", "-t", at.Pane, "-l", "--", tmuxLine(text)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	return m.press(ctx, env, at, "enter")
}

func (tmuxMux) press(ctx context.Context, env *Env, at Location, key string) error {
	name := map[string]string{"enter": "Enter", "down": "Down"}[key]
	if name == "" {
		return fmt.Errorf("unsupported key %q", key)
	}
	_, err := env.Run(ctx, nil, env.Bin("tmux"), "send-keys", "-t", at.Pane, name)
	return err
}

// tmuxLine joins lines with spaces, since a newline would submit early, but
// keeps the spacing within each line (a literal paste preserves it).
func tmuxLine(text string) string {
	return strings.Join(strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), " ")
}
