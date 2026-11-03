package agents

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// tmuxSubmit types text into a pane as a literal paste and presses Enter.
func tmuxSubmit(ctx context.Context, env *Env, pane, text string) error {
	if pane == "" {
		return fmt.Errorf("session is not running in a tmux pane")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Newlines would submit early in most TUIs; send the message as one line.
	text = strings.Join(strings.Split(text, "\n"), " ")
	if _, err := env.Run(ctx, nil, env.Bin("tmux"), "send-keys", "-t", pane, "-l", "--", text); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	_, err := env.Run(ctx, nil, env.Bin("tmux"), "send-keys", "-t", pane, "Enter")
	return err
}

// paneRef is a terminal pane an agent runs in, when one can be typed into.
type paneRef struct {
	backend string // "tmux" or "herdr"
	id      string
}

// resolvePane finds the tmux pane on tty or the herdr pane running pid.
func resolvePane(ctx context.Context, env *Env, pid int, tty string) paneRef {
	if id := env.tmuxPane(ctx, tty); id != "" {
		return paneRef{"tmux", id}
	}
	if id := env.herdrPane(ctx, pid); id != "" {
		return paneRef{"herdr", id}
	}
	return paneRef{}
}

func (p paneRef) describe() string {
	if p.backend == "" {
		return ""
	}
	return p.backend + " pane " + p.id
}

// submitToPane types text into the pane and presses Enter.
func submitToPane(ctx context.Context, env *Env, p paneRef, text string) error {
	switch p.backend {
	case "tmux":
		return tmuxSubmit(ctx, env, p.id, text)
	case "herdr":
		return herdrSubmit(ctx, env, p.id, text)
	}
	return fmt.Errorf("no pane to type into")
}
