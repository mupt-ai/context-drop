package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// herdrMux drives herdr through its socket API CLI. Commands reach the herdr
// session the caller runs in (HERDR_SESSION, HERDR_SOCKET_PATH), or the
// default one.
type herdrMux struct{}

type herdrPane struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Focused     bool   `json:"focused"`
}

type herdrNamed struct {
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Label       string `json:"label"`
}

func herdrList[T any](ctx context.Context, env *Env, field string, args ...string) []T {
	out, err := env.Run(ctx, nil, env.Bin("herdr"), args...)
	if err != nil {
		return nil
	}
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	var items []T
	if json.Unmarshal(out, &resp) == nil {
		_ = json.Unmarshal(resp.Result[field], &items)
	}
	return items
}

func (herdrMux) open(ctx context.Context, env *Env, at Placement, dir, label string, argv []string) (Location, error) {
	herdr := env.Bin("herdr")
	workspace := herdrResolve(ctx, env, "workspace", at.Session, "")
	var pane herdrPane
	if at.Pane == "" && at.Window == "" {
		args := []string{"tab", "create", "--cwd", dir, "--label", label}
		if workspace != "" {
			args = append(args, "--workspace", workspace)
		}
		out, err := env.Run(ctx, nil, herdr, args...)
		if err != nil {
			return Location{}, err
		}
		var created struct {
			Result struct {
				RootPane herdrPane `json:"root_pane"`
			} `json:"result"`
		}
		if json.Unmarshal(out, &created) != nil || created.Result.RootPane.PaneID == "" {
			return Location{}, fmt.Errorf("herdr did not report the new pane: %s", oneLine(string(out), 200))
		}
		pane = created.Result.RootPane
	} else {
		anchor := at.Pane
		if anchor == "" {
			var err error
			if anchor, err = herdrTabPane(ctx, env, herdrResolve(ctx, env, "tab", at.Window, workspace)); err != nil {
				return Location{}, err
			}
		}
		out, err := env.Run(ctx, nil, herdr, "pane", "split", anchor, "--direction", "right", "--cwd", dir)
		if err != nil {
			return Location{}, err
		}
		var created struct {
			Result struct {
				Pane herdrPane `json:"pane"`
			} `json:"result"`
		}
		if json.Unmarshal(out, &created) != nil || created.Result.Pane.PaneID == "" {
			return Location{}, fmt.Errorf("herdr did not report the new pane: %s", oneLine(string(out), 200))
		}
		pane = created.Result.Pane
		_, _ = env.Run(ctx, nil, herdr, "pane", "rename", pane.PaneID, label)
	}
	loc := Location{Mux: MuxHerdr, Session: pane.WorkspaceID, Window: pane.TabID, Pane: pane.PaneID, Label: label}
	// The pane starts a shell; run the agent in it so the user's PATH and profile apply.
	_, err := env.Run(ctx, nil, herdr, "pane", "run", pane.PaneID, shellJoin(argv))
	return loc, err
}

// herdrResolve turns a workspace or tab label into its ID. An unknown name is
// returned unchanged so herdr reports it.
func herdrResolve(ctx context.Context, env *Env, kind, name, workspace string) string {
	if name == "" {
		return ""
	}
	args := []string{kind, "list"}
	if kind == "tab" && workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	byLabel := ""
	for _, item := range herdrList[herdrNamed](ctx, env, kind+"s", args...) {
		id := item.WorkspaceID
		if kind == "tab" {
			id = item.TabID
		}
		if id == name {
			return id // an exact ID wins over any label
		}
		if byLabel == "" && strings.EqualFold(item.Label, name) {
			byLabel = id
		}
	}
	return firstNonEmpty(byLabel, name)
}

// herdrTabPane picks the pane to split in a tab: its focused pane, else its first.
func herdrTabPane(ctx context.Context, env *Env, tab string) (string, error) {
	var first string
	for _, p := range herdrList[herdrPane](ctx, env, "panes", "pane", "list") {
		if p.TabID != tab {
			continue
		}
		if p.Focused {
			return p.PaneID, nil
		}
		if first == "" {
			first = p.PaneID
		}
	}
	if first == "" {
		return "", fmt.Errorf("herdr tab %q not found", tab)
	}
	return first, nil
}

func (herdrMux) locate(ctx context.Context, env *Env, pid int, _ string) (Location, bool) {
	return ancestorPane(ctx, env, pid, env.herdrPanes(ctx))
}

// herdrPanes maps each herdr pane's shell and foreground processes to the pane.
func (e *Env) herdrPanes(ctx context.Context) map[int]Location {
	e.herdrOnce.Do(func() {
		e.herdr = map[int]Location{}
		panes := herdrList[herdrPane](ctx, e, "panes", "pane", "list")
		if len(panes) == 0 {
			return
		}
		labels := map[string]string{}
		for _, w := range herdrList[herdrNamed](ctx, e, "workspaces", "workspace", "list") {
			labels[w.WorkspaceID] = w.Label
		}
		for _, t := range herdrList[herdrNamed](ctx, e, "tabs", "tab", "list") {
			labels[t.TabID] = t.Label
		}
		for _, pane := range panes {
			info, err := e.Run(ctx, nil, e.Bin("herdr"), "pane", "process-info", "--pane", pane.PaneID)
			if err != nil {
				continue
			}
			var procs struct {
				Result struct {
					ProcessInfo struct {
						ShellPID   int `json:"shell_pid"`
						Foreground []struct {
							PID int `json:"pid"`
						} `json:"foreground_processes"`
					} `json:"process_info"`
				} `json:"result"`
			}
			if json.Unmarshal(info, &procs) != nil {
				continue
			}
			loc := Location{Mux: MuxHerdr, Session: pane.WorkspaceID, Window: pane.TabID, Pane: pane.PaneID,
				aliases: [3][]string{{labels[pane.WorkspaceID]}, {labels[pane.TabID]}, nil}}
			pi := procs.Result.ProcessInfo
			e.herdr[pi.ShellPID] = loc
			for _, p := range pi.Foreground {
				e.herdr[p.PID] = loc
			}
		}
	})
	return e.herdr
}

func (herdrMux) capture(ctx context.Context, env *Env, at Location, lines int) (string, error) {
	args := []string{"pane", "read", at.Pane, "--source", "recent", "--format", "text"}
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
	}
	out, err := env.Run(ctx, nil, env.Bin("herdr"), args...)
	return lastLines(string(out), lines), err
}

func (m herdrMux) submit(ctx context.Context, env *Env, at Location, text string) error {
	if _, err := env.Run(ctx, nil, env.Bin("herdr"), "pane", "send-text", at.Pane, singleLine(text)); err != nil {
		return err
	}
	return m.press(ctx, env, at, "enter")
}

func (herdrMux) press(ctx context.Context, env *Env, at Location, key string) error {
	_, err := env.Run(ctx, nil, env.Bin("herdr"), "pane", "send-keys", at.Pane, key)
	return err
}

func (herdrMux) close(ctx context.Context, env *Env, at Location) error {
	_, err := env.Run(ctx, nil, env.Bin("herdr"), "pane", "close", at.Pane)
	return err
}
