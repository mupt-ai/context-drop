package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"contextdrop.dev/context-drop/internal/config"
	"github.com/spf13/cobra"
)

func newStartCommand() *cobra.Command {
	var dir, name, in, command, prompt string
	var wait, asJSON bool
	var timeout, waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "start AGENT [-- AGENT_ARGS...]",
		Short: "Start a new agent in a tmux window, herdr tab, or rex block",
		Long: "Start claude, codex, pi, or opencode in a new tmux window (session \"context-drop\"), herdr tab, or rex\n" +
			"block, wait until it appears in `context-drop list`, and print its ID.\n\n" +
			"--in picks the multiplexer; the default is launch_in in ~/.context-drop/config.toml, then the one you are\n" +
			"running inside, then whichever is installed. Arguments after -- go to the agent.\n\n" +
			"--command (or launch_command in config) is the command line that starts the agent instead of its bare\n" +
			"executable; {agent} becomes the agent name, so launch_command = \"dari {agent}\" runs `dari claude`,\n" +
			"`dari codex`, and so on. The agent kind still decides how the session is found.\n\n" +
			"--prompt sends a first message once the agent is up (OpenCode receives it on its command line, because it\n" +
			"only creates a session when it gets one).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := strings.ToLower(args[0])
			var agentArgs []string
			if at := cmd.ArgsLenAtDash(); at >= 0 {
				if at != 1 {
					return fmt.Errorf("put agent arguments after --, e.g. context-drop start claude -- --model x")
				}
				agentArgs = args[1:]
			} else if len(args) > 1 {
				return fmt.Errorf("unexpected arguments %q; put agent arguments after --", args[1:])
			}
			abs, err := filepath.Abs(firstNonEmptyString(dir, "."))
			if err != nil {
				return err
			}
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				return fmt.Errorf("--cwd %s is not a directory", abs)
			}
			if cfg, err := config.LoadCLIConfig(); err == nil {
				in = firstNonEmptyString(in, cfg.LaunchIn)
				command = firstNonEmptyString(command, cfg.LaunchCommand)
			}
			if name == "" {
				name = kind + "-" + filepath.Base(abs)
			}
			res, err := agents.Launch(cmd.Context(), newEnv, allProviders(), agents.LaunchOptions{
				Kind: kind, Dir: abs, Name: name, Mux: strings.ToLower(in), Command: command, Args: agentArgs,
				Prompt: prompt, Timeout: timeout,
			})
			if err != nil {
				if res.Pane != "" {
					return fmt.Errorf("%w (opened %s pane %s)", err, res.Mux, res.Pane)
				}
				return err
			}
			if !res.Registered {
				msg := fmt.Sprintf("started %s in %s pane %s, but it has not appeared in `context-drop list` after %s", kind, res.Mux, res.Pane, timeout)
				if kind == agents.OpenCode && prompt == "" {
					msg += "; OpenCode creates its session when it gets its first message"
				}
				if prompt != "" && kind != agents.OpenCode {
					return fmt.Errorf("%s; the prompt was not sent", msg)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), msg)
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), res)
				}
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "started %s in %s pane %s\n", res.Session.ID, res.Mux, res.Pane)
			if prompt != "" && kind == agents.OpenCode && wait {
				// OpenCode got the prompt on its command line; wait for its reply.
				// A brand-new session has no earlier copy of the prompt.
				final, err := waitForPrompt(cmd, res.Session, prompt, time.Now().Add(-timeout), 0, waitTimeout)
				if err != nil {
					return err
				}
				res.Session = final
			}
			if prompt != "" && kind != agents.OpenCode {
				final, err := sendPrompt(cmd, res.Session, prompt, wait, waitTimeout)
				if err != nil {
					return err
				}
				if final.ID != res.Session.ID {
					// A Pi session without the extension takes its file's ID once it gets a message.
					fmt.Fprintf(cmd.ErrOrStderr(), "%s is now %s\n", res.Session.ID, final.ID)
					res.Session = final
				}
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.Session.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "cwd", "", "directory to start the agent in (default: current directory)")
	cmd.Flags().StringVar(&name, "name", "", "label for the window, tab, or block (default: AGENT-DIRNAME)")
	cmd.Flags().StringVar(&in, "in", "", "multiplexer: tmux, herdr, or rex")
	cmd.Flags().StringVar(&command, "command", "", `command line that starts the agent, e.g. "dari {agent}"`)
	cmd.Flags().StringVar(&prompt, "prompt", "", "first message to send once the agent is up")
	cmd.Flags().BoolVar(&wait, "wait", false, "with --prompt, wait for the reply and print it")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "how long to wait for the agent to appear")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 30*time.Minute, "how long --wait waits for the reply")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the launch result as JSON")
	return cmd
}

// sendPrompt delivers the first message to a freshly started session.
// It returns the session as it is afterwards, whose ID may have changed.
func sendPrompt(cmd *cobra.Command, s agents.Session, prompt string, wait bool, waitTimeout time.Duration) (agents.Session, error) {
	env, providers, idx, err := discover(cmd.Context())
	if err != nil {
		return s, err
	}
	target, err := idx.Resolve(s.ID)
	if err != nil {
		return s, err
	}
	sentAt := time.Now()
	since := sentAt.Add(-5 * time.Second)
	prior := 0
	if wait {
		prior = priorMatches(cmd.Context(), env, providers, idx, target, prompt, since)
	}
	got, err := agents.Send(cmd.Context(), env, providers, idx, target, prompt, agents.SendOptions{})
	if err != nil {
		return s, fmt.Errorf("started %s but sending the prompt failed: %w", s.ID, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "sent the prompt via %s\n", got.Delivery)
	if !wait {
		// The first message can change a Pi session's ID; report the one that stays live.
		return agents.Settle(cmd.Context(), newEnv, providers, got, 30*time.Second, 0), nil
	}
	return waitForPrompt(cmd, got, prompt, since, prior, waitTimeout)
}

// waitForPrompt waits for the reply to prompt in s and prints it.
// prior is the number of messages that already looked like prompt before it was sent.
func waitForPrompt(cmd *cobra.Command, s agents.Session, prompt string, since time.Time, prior int, waitTimeout time.Duration) (agents.Session, error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
	defer cancel()
	msgs, final, err := agents.WaitForReply(ctx, newEnv, allProviders(), s, agents.WaitOptions{Timeout: waitTimeout, Sent: prompt, Since: since, PriorMatches: prior})
	if err != nil {
		return s, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s is %s\n", final.ID, final.Status)
	printMessages(cmd.ErrOrStderr(), msgs)
	return final, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
