// Package herdr is a thin client for the herdr CLI's JSON socket API.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes the herdr binary and returns its stdout.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

type Client struct {
	Run Runner
}

func New(bin string) Client {
	return Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = strings.TrimSpace(stdout.String())
			}
			return nil, fmt.Errorf("herdr %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, msg)
		}
		return stdout.Bytes(), nil
	}}
}

type Agent struct {
	Name        string `json:"name,omitempty"`
	Kind        string `json:"agent"`
	Status      string `json:"agent_status"`
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Cwd         string `json:"cwd"`
	Title       string `json:"terminal_title_stripped"`
}

type Workspace struct {
	ID    string `json:"workspace_id"`
	Label string `json:"label"`
}

func (c Client) call(ctx context.Context, result any, args ...string) error {
	out, err := c.Run(ctx, args...)
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return fmt.Errorf("herdr %s: invalid JSON: %w", strings.Join(args[:min(2, len(args))], " "), err)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

func (c Client) Agents(ctx context.Context) ([]Agent, error) {
	var result struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.call(ctx, &result, "agent", "list"); err != nil {
		return nil, err
	}
	return result.Agents, nil
}

func (c Client) Agent(ctx context.Context, target string) (Agent, error) {
	var result struct {
		Agent Agent `json:"agent"`
	}
	err := c.call(ctx, &result, "agent", "get", target)
	return result.Agent, err
}

func (c Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var result struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := c.call(ctx, &result, "workspace", "list"); err != nil {
		return nil, err
	}
	return result.Workspaces, nil
}

// CreateTab opens a tab without taking focus and returns its root pane.
func (c Client) CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error) {
	var result struct {
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := c.call(ctx, &result, "tab", "create", "--workspace", workspaceID, "--cwd", cwd, "--label", label, "--no-focus"); err != nil {
		return "", err
	}
	if result.RootPane.PaneID == "" {
		return "", errors.New("herdr tab create returned no pane ID")
	}
	return result.RootPane.PaneID, nil
}

func (c Client) RunInPane(ctx context.Context, pane, command string) error {
	_, err := c.Run(ctx, "pane", "run", pane, command)
	return err
}

func (c Client) PaneKeys(ctx context.Context, pane string, keys ...string) error {
	_, err := c.Run(ctx, append([]string{"pane", "send-keys", pane}, keys...)...)
	return err
}

func (c Client) Rename(ctx context.Context, target, name string) error {
	_, err := c.Run(ctx, "agent", "rename", target, name)
	return err
}

// Prompt submits text. With wait it blocks until herdr sees the agent settle
// (idle, done, or blocked) after the submission started work.
func (c Client) Prompt(ctx context.Context, target, text string, wait bool, timeout time.Duration) error {
	args := []string{"agent", "prompt", target, text}
	if wait {
		args = append(args, "--wait")
		if timeout > 0 {
			args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
		}
	}
	_, err := c.Run(ctx, args...)
	return err
}

// Wait blocks until the agent is idle, done, or blocked.
func (c Client) Wait(ctx context.Context, target string, timeout time.Duration) error {
	args := []string{"agent", "wait", target}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	_, err := c.Run(ctx, args...)
	return err
}

func (c Client) Read(ctx context.Context, target string, lines int) (string, error) {
	out, err := c.Run(ctx, "agent", "read", target, "--lines", strconv.Itoa(lines))
	return string(out), err
}
