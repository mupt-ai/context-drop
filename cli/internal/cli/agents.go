package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"contextdrop.dev/context-drop/internal/herdr"
	"github.com/spf13/cobra"
)

var agentName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

const outputLines = 60

var newHerdr = func() herdr.Client {
	if bin := os.Getenv("HERDR_BIN"); bin != "" {
		return herdr.New(bin)
	}
	return herdr.New("herdr")
}

func newAgentsCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List coding agents running in herdr",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agents, err := newHerdr().Agents(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(agents)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TARGET\tKIND\tSTATUS\tTITLE\tCWD")
			for _, a := range agents {
				target := a.PaneID
				if a.Name != "" {
					target = a.Name
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", target, a.Kind, a.Status, a.Title, a.Cwd)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newNewCommand() *cobra.Command {
	var workspace, cwd, kind string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "new NAME PROMPT",
		Short: "Start a coding agent in a new herdr tab and give it a task",
		Long: "Opens a tab in the herdr workspace with the given label, starts the agent using herdr, " +
			"and submits PROMPT. Create any worktree first and pass it as --cwd.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNew(cmd, args[0], args[1], workspace, cwd, kind, wait, timeout)
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "label of the herdr workspace to open the tab in (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "absolute directory to run the agent in (required)")
	cmd.Flags().StringVar(&kind, "agent", "claude", "agent to launch: claude, codex, or pi")
	cmd.Flags().BoolVar(&wait, "wait", false, "block until the agent finishes its turn or needs input, then print its output")
	cmd.Flags().DurationVar(&timeout, "timeout", 6*time.Hour, "maximum time to wait with --wait")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("cwd")
	return cmd
}

func runNew(cmd *cobra.Command, name, prompt, label, cwd, kind string, wait bool, timeout time.Duration) error {
	ctx := cmd.Context()
	h := newHerdr()
	if kind != "claude" && kind != "codex" && kind != "pi" {
		return fmt.Errorf("unknown agent %q; use claude, codex, or pi", kind)
	}
	if !agentName.MatchString(name) {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits, - or _, starting with a letter (max 32)", name)
	}
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("--cwd must be an absolute path")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return fmt.Errorf("--cwd %s is not a directory", cwd)
	}
	agents, err := h.Agents(ctx)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.Name == name {
			return fmt.Errorf("an agent named %s already exists at %s; use send to continue it", name, a.PaneID)
		}
	}
	workspaceID, err := resolveWorkspace(ctx, h, label)
	if err != nil {
		return err
	}
	pane, err := h.CreateTab(ctx, workspaceID, cwd, name)
	if err != nil {
		return err
	}
	if err := h.Start(ctx, name, kind, pane); err != nil {
		return fmt.Errorf("start %s: %w (tab left open at %s)", kind, err, pane)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "started %s (%s) at %s in %s\n", name, kind, pane, cwd)
	return submit(cmd, h, name, prompt, wait, timeout)
}

func resolveWorkspace(ctx context.Context, h herdr.Client, label string) (string, error) {
	workspaces, err := h.Workspaces(ctx)
	if err != nil {
		return "", err
	}
	var labels []string
	var matches []string
	for _, w := range workspaces {
		labels = append(labels, w.Label)
		if w.Label == label {
			matches = append(matches, w.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no herdr workspace labeled %q; have: %s", label, strings.Join(labels, ", "))
	default:
		return "", fmt.Errorf("%d herdr workspaces are labeled %q; rename one", len(matches), label)
	}
}

func newSendCommand() *cobra.Command {
	var force, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "send TARGET TEXT",
		Short: "Send a prompt to an existing herdr agent (by name or pane ID)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			h := newHerdr()
			a, err := h.Agent(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.Status != "idle" && a.Status != "done" && !force {
				return fmt.Errorf("%s is %s; wait for it or pass --force", args[0], a.Status)
			}
			return submit(cmd, h, args[0], args[1], wait, timeout)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "send even if the agent is not idle")
	cmd.Flags().BoolVar(&wait, "wait", false, "block until the agent finishes its turn or needs input, then print its output")
	cmd.Flags().DurationVar(&timeout, "timeout", 6*time.Hour, "maximum time to wait with --wait")
	return cmd
}

func submit(cmd *cobra.Command, h herdr.Client, target, text string, wait bool, timeout time.Duration) error {
	if err := h.Prompt(cmd.Context(), target, text, wait, timeout); err != nil {
		return err
	}
	if !wait {
		fmt.Fprintf(cmd.OutOrStdout(), "sent to %s\n", target)
		return nil
	}
	return printSettled(cmd, h, target)
}

func newWaitCommand() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait TARGET",
		Short: "Wait until a working agent finishes or needs input, then print its output",
		Long:  "Returns immediately if the agent is already idle. To wait on a prompt you are sending, use send --wait instead.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h := newHerdr()
			if err := h.Wait(cmd.Context(), args[0], timeout); err != nil {
				return err
			}
			return printSettled(cmd, h, args[0])
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 6*time.Hour, "maximum time to wait")
	return cmd
}

func newReadCommand() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "read TARGET",
		Short: "Print an agent's recent terminal output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := readOutput(cmd.Context(), newHerdr(), args[0], lines)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		},
	}
	cmd.Flags().IntVar(&lines, "lines", outputLines, "number of non-blank lines")
	return cmd
}

func printSettled(cmd *cobra.Command, h herdr.Client, target string) error {
	a, err := h.Agent(cmd.Context(), target)
	if err != nil {
		return err
	}
	text, err := readOutput(cmd.Context(), h, target, outputLines)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s is %s\n\n%s", target, a.Status, text)
	return nil
}

// readOutput returns the last n non-blank lines. Agent TUIs pad the screen
// with blank rows, so a plain line count mostly returns padding.
func readOutput(ctx context.Context, h herdr.Client, target string, n int) (string, error) {
	text, err := h.Read(ctx, target, n*5)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}
