package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"github.com/spf13/cobra"
)

// Seams for tests.
var (
	newEnv       = agents.NewEnv
	allProviders = agents.Providers
)

func discover(ctx context.Context) (*agents.Env, []agents.Provider, agents.Index, error) {
	env, err := newEnv()
	if err != nil {
		return nil, nil, agents.Index{}, err
	}
	providers := allProviders()
	return env, providers, agents.Discover(ctx, env, providers), nil
}

func printWarnings(w io.Writer, idx agents.Index) {
	for _, warning := range idx.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func newListCommand() *cobra.Command {
	var asJSON, topLevel bool
	var agentFilter string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List live agent sessions and subagents with their IDs",
		Long: "List every live Claude Code, Codex, Pi, OpenCode, and Muse session on this machine, including subagents.\n" +
			"IDs are deterministic: the same session always gets the same ID.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			printWarnings(cmd.ErrOrStderr(), idx)
			var sessions []agents.Session
			for _, s := range idx.Sessions {
				if (agentFilter == "" || s.Agent == agentFilter) && (!topLevel || !s.Subagent) {
					sessions = append(sessions, s)
				}
			}
			if asJSON {
				if sessions == nil {
					sessions = []agents.Session{}
				}
				return writeJSON(cmd.OutOrStdout(), sessions)
			}
			if len(sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No live agent sessions.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAGENT\tSTATUS\tACTIVE\tCWD\tNAME")
			for _, s := range sessions {
				id := s.ID
				if s.Subagent {
					id = "└ " + id
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", id, s.Agent, s.Status, ago(s.UpdatedAt), shortPath(s.Cwd), label(s))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&topLevel, "top-level", false, "hide subagents")
	cmd.Flags().StringVar(&agentFilter, "agent", "", "only show one agent kind (claude, codex, pi, opencode, muse)")
	return cmd
}

func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show TARGET",
		Short: "Print everything known about one session as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			s, err := idx.Resolve(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), s)
		},
	}
}

func newReadCommand() *cobra.Command {
	var count int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "read TARGET",
		Short: "Print the recent transcript of a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, providers, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			s, err := idx.Resolve(args[0])
			if err != nil {
				return err
			}
			p, err := agents.ProviderFor(providers, s.Agent)
			if err != nil {
				return err
			}
			msgs, err := p.Transcript(cmd.Context(), env, s)
			if err != nil {
				return err
			}
			msgs = lastMessages(msgs, count)
			if asJSON {
				if msgs == nil {
					msgs = []agents.Message{}
				}
				return writeJSON(cmd.OutOrStdout(), msgs)
			}
			printMessages(cmd.OutOrStdout(), msgs)
			return nil
		},
	}
	cmd.Flags().IntVarP(&count, "messages", "n", 20, "number of recent messages to print (0 for all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newSendCommand() *cobra.Command {
	var from string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "send TARGET MESSAGE...",
		Short: "Send a message to a live session or subagent",
		Long: "Deliver MESSAGE into a live session as if it were the next prompt. Use - as MESSAGE to read it from stdin.\n\n" +
			"Delivery uses each agent's own channel: Claude Code's peer socket, `codex queue`, the OpenCode service API,\n" +
			"the Context Drop Pi extension (see `context-drop setup pi`), and for Muse its tmux pane or `muse session-message`.\n" +
			"Claude Code and Muse subagents receive messages through their parent session.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args[1:], " ")
			if text == "-" {
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(data)
			}
			text = strings.TrimSpace(text)
			env, providers, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			target, err := idx.Resolve(args[0])
			if err != nil {
				return err
			}
			sentAt := time.Now()
			got, err := agents.Send(cmd.Context(), env, providers, idx, target, text, agents.SendOptions{From: from})
			if err != nil {
				return err
			}
			if got.ID != target.ID {
				fmt.Fprintf(cmd.ErrOrStderr(), "relayed to %s through parent %s via %s\n", target.ID, got.ID, got.Delivery)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "sent to %s via %s\n", got.ID, got.Delivery)
			}
			if !wait {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			msgs, final, err := agents.WaitForReply(ctx, newEnv, providers, got, agents.WaitOptions{
				Timeout: timeout, Sent: text, Since: sentAt.Add(-5 * time.Second),
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%s is %s\n", final.ID, final.Status)
			printMessages(cmd.OutOrStdout(), msgs)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "sender name shown to agents that display it (default context-drop)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the reply and print it")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

func newSetupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install agent integrations Context Drop needs",
	}
	var printOnly bool
	pi := &cobra.Command{
		Use:   "pi",
		Short: "Install the Pi extension that lets context-drop list and message live Pi sessions",
		Long: "Writes the Context Drop extension to ~/.pi/agent/extensions/context-drop.ts. Pi sessions started after\n" +
			"this publish themselves to ~/.context-drop/pi and accept messages from `context-drop send`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printOnly {
				_, err := cmd.OutOrStdout().Write(agents.PiExtension)
				return err
			}
			env, err := newEnv()
			if err != nil {
				return err
			}
			path := env.PiExtensionPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, agents.PiExtension, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s; restart running pi sessions to load it\n", path)
			return nil
		},
	}
	pi.Flags().BoolVar(&printOnly, "print", false, "print the extension instead of installing it")
	cmd.AddCommand(pi)
	return cmd
}

func lastMessages(msgs []agents.Message, n int) []agents.Message {
	if n > 0 && len(msgs) > n {
		return msgs[len(msgs)-n:]
	}
	return msgs
}

func printMessages(w io.Writer, msgs []agents.Message) {
	if len(msgs) == 0 {
		fmt.Fprintln(w, "(no messages)")
		return
	}
	for i, m := range msgs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		stamp := ""
		if !m.Time.IsZero() {
			stamp = " · " + m.Time.Local().Format("Jan 2 15:04")
		}
		fmt.Fprintf(w, "── %s%s\n%s\n", m.Role, stamp, m.Text)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func label(s agents.Session) string {
	parts := []string{}
	if s.Name != "" {
		parts = append(parts, s.Name)
	}
	if s.Title != "" && s.Title != s.Name {
		parts = append(parts, s.Title)
	}
	return truncateRunes(strings.Join(strings.Fields(strings.Join(parts, " — ")), " "), 70)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
