package agents

import (
	"context"
	"encoding/json"
	"strings"
)

// herdrPanes maps each herdr pane's shell and foreground processes to the
// pane ID. Herdr is only used to deliver into and launch in panes it runs;
// discovery never depends on it.
func (e *Env) herdrPanes(ctx context.Context) map[int]string {
	e.herdrOnce.Do(func() {
		e.herdr = map[int]string{}
		out, err := e.Run(ctx, nil, e.Bin("herdr"), "pane", "list")
		if err != nil {
			return
		}
		var list struct {
			Result struct {
				Panes []struct {
					PaneID string `json:"pane_id"`
				} `json:"panes"`
			} `json:"result"`
		}
		if json.Unmarshal(out, &list) != nil {
			return
		}
		for _, pane := range list.Result.Panes {
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
			pi := procs.Result.ProcessInfo
			e.herdr[pi.ShellPID] = pane.PaneID
			for _, p := range pi.Foreground {
				e.herdr[p.PID] = pane.PaneID
			}
		}
	})
	return e.herdr
}

// herdrSubmit types text into a herdr pane and presses Enter.
func herdrSubmit(ctx context.Context, env *Env, pane, text string) error {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\r\n", "\n")), " ")
	if _, err := env.Run(ctx, nil, env.Bin("herdr"), "pane", "send-text", pane, text); err != nil {
		return err
	}
	_, err := env.Run(ctx, nil, env.Bin("herdr"), "pane", "send-keys", pane, "enter")
	return err
}
