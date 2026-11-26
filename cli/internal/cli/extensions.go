package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/extensions/linear"
	"contextdrop.dev/context-drop/internal/extensions/slack"
	"contextdrop.dev/context-drop/internal/pool"
)

// extensionCLI is what extensions' commands get from the CLI.
func extensionCLI() extensions.CLI {
	return extensions.CLI{
		Org: orgMembership,
		Server: func() string {
			if m, ok, _ := joined(); ok {
				return m.Server
			}
			return pool.DefaultServer
		},
		Agent: callerAgent,
		JSON:  writeJSON,
	}
}

// extensionCommands are `context-drop slack`, `context-drop linear`, and the like.
func extensionCommands() []*cobra.Command {
	env := extensionCLI()
	return []*cobra.Command{slack.Command(env), linear.Command(env)}
}

// sendToPeople sends to a person-facing target through the extension that owns it.
func sendToPeople(cmd *cobra.Command, target, text, from string, wait bool, timeout time.Duration) error {
	env := extensionCLI()
	if slack.IsTarget(target) {
		return slack.Send(cmd, env, target, text, from, wait, timeout)
	}
	if wait {
		return fmt.Errorf("--wait works for Slack only; a reply on %s comes back to the sending agent as its next prompt", target)
	}
	c, _, err := env.Client()
	if err != nil {
		return err
	}
	out, err := c.Send(cmd.Context(), pool.SendRequest{To: target, Text: text, From: from, Agent: env.Agent(cmd.Context())})
	if err != nil {
		return err
	}
	for _, r := range out.Results {
		fmt.Fprintf(cmd.ErrOrStderr(), "sent to %s via %s\n", r.ID, r.Via)
		fmt.Fprintln(cmd.OutOrStdout(), r.ID)
	}
	return nil
}
