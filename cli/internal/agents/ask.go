package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// AskTimeout bounds one Ask.
const AskTimeout = 5 * time.Minute

// Ask answers a question from a copy of a Claude Code session: the session is forked, the copy answers
// with read-only tools, and nothing is saved, so the session itself is never interrupted or changed.
// It suits questions about what the agent knows or has done; anything the agent must agree to or act
// on is a message (Send).
func Ask(ctx context.Context, env *Env, s Session, question string, opts SendOptions) (string, error) {
	if strings.TrimSpace(question) == "" {
		return "", errors.New("question is empty")
	}
	if s.Agent != Claude {
		return "", fmt.Errorf("%s is a %s session; only Claude Code sessions can be asked for now, so send it a message", s.ID, s.Agent)
	}
	if s.Subagent {
		return "", fmt.Errorf("%s is a subagent, which cannot be copied; ask its parent or send it a message", s.ID)
	}
	if s.NativeID == "" {
		return "", fmt.Errorf("%s has no session ID to copy", s.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, AskTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, env.Bin("claude"), "-p",
		"--resume", s.NativeID, "--fork-session", "--no-session-persistence",
		"--tools", "Read,Grep,Glob",
		"--output-format", "text",
		askPrompt(question, opts))
	cmd.Dir = s.Cwd
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("no answer from %s within %s", s.ID, AskTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("asking %s: %s", s.ID, oneLine(msg, 300))
	}
	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return "", fmt.Errorf("%s gave an empty answer", s.ID)
	}
	return answer, nil
}

// askPrompt tells the copy who is asking and that it is a copy, so it answers instead of working.
func askPrompt(question string, opts SendOptions) string {
	who := "Someone"
	switch {
	case strings.TrimSpace(opts.ReplyTo) != "":
		who = "Agent " + opts.ReplyTo + " (not your user)"
	case strings.TrimSpace(opts.From) != "":
		who = strings.TrimSpace(opts.From) + " (not your user)"
	}
	return "[context-drop] " + who + " asks you a question. You are a temporary copy of this session: answer from what " +
		"you already know and can read, briefly, and do not change anything. The real session is not interrupted " +
		"and will not see this.\n\n" + question
}
