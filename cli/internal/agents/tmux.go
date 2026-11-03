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
