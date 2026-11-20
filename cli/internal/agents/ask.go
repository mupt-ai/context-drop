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

// askModel answers questions; reading a transcript does not need a large model.
const askModel = "haiku"

// askBudget is how much of a transcript, from its end, the answerer is given (about 100k tokens).
const askBudget = 400_000

// Ask answers a question about a session from its transcript, so the session is never interrupted.
// A fresh Claude Code run reads the transcript as text, with read-only tools in the session's
// directory and no hooks or MCP servers, and saves nothing. It works for every kind of agent.
// It suits questions about what the agent knows or has done; anything the agent must agree to or act
// on is a message (Send).
func Ask(ctx context.Context, env *Env, providers []Provider, s Session, question string, opts SendOptions) (string, error) {
	if strings.TrimSpace(question) == "" {
		return "", errors.New("question is empty")
	}
	p, err := ProviderFor(providers, s.Agent)
	if err != nil {
		return "", err
	}
	msgs, err := p.Transcript(ctx, env, s)
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", fmt.Errorf("%s has no transcript yet", s.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, AskTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, env.Bin("claude"), "-p",
		"--model", askModel,
		"--no-session-persistence",
		// Read-only, and nothing of the project's runs: no hooks (they could change the workspace) and no
		// MCP servers (one could message the very session that must not be interrupted).
		"--tools", "Read,Grep,Glob",
		"--settings", `{"disableAllHooks":true}`,
		"--strict-mcp-config",
		"--output-format", "text")
	cmd.Dir = s.Cwd
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(askPrompt(s, msgs, question, opts))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("no answer about %s within %s", s.ID, AskTimeout)
		}
		msg := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("asking %s: %s", s.ID, oneLine(msg, 300))
	}
	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return "", fmt.Errorf("empty answer about %s", s.ID)
	}
	return answer, nil
}

// askPrompt gives the answerer the question, who asks it, and the end of the transcript.
func askPrompt(s Session, msgs []Message, question string, opts SendOptions) string {
	who := "Someone"
	switch {
	case strings.TrimSpace(opts.ReplyTo) != "":
		who = "Agent " + opts.ReplyTo
	case strings.TrimSpace(opts.From) != "":
		who = strings.TrimSpace(opts.From)
	}
	var blocks []string
	size := 0
	for i := len(msgs) - 1; i >= 0 && size < askBudget; i-- {
		b := "[" + msgs[i].Role + "]\n" + msgs[i].Text
		blocks = append(blocks, b)
		size += len(b)
	}
	var t strings.Builder
	t.WriteString("[context-drop] " + who + " has a question for agent " + s.ID + ". Below is that agent's transcript, oldest first: " +
		"its user's prompts and its replies (tool calls are not included")
	if size >= askBudget {
		t.WriteString("; earlier parts are cut")
	}
	t.WriteString(").\n\nAnswer as that agent would, briefly, from the transcript. If it does not say, you may read files in the " +
		"agent's working directory. If you still cannot tell, say so instead of guessing. Do not change anything.\n\n<transcript>\n")
	for i := len(blocks) - 1; i >= 0; i-- {
		t.WriteString(blocks[i] + "\n\n")
	}
	t.WriteString("</transcript>\n\nQuestion: " + question)
	return t.String()
}
