package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"contextdrop.dev/context-drop/internal/pool"
)

// openBrowser opens a link in the person's default browser. The caller prints the link as well.
func openBrowser(link string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, link).Start()
}

// slackConnectTimeout is how long `slack connect` waits for the sign-in to finish.
var slackConnectTimeout = 10 * time.Minute

// slackConnectEvery is how often `slack connect` checks whether the sign-in finished.
var slackConnectEvery = 2 * time.Second

func newSlackConnectCommand() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Link your own Slack account to this org, so agents can reach you on Slack",
		Long: "Opens Slack in your browser. Sign in, and your Slack account is linked to the org: agents can message you,\n" +
			"and your DMs and thread replies reach them. Only you can link yourself this way; `slack allow` links someone else.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := orgMembership()
			if err != nil {
				return err
			}
			c := pool.Client{Membership: m}
			var start struct{ URL string }
			if err := c.Post(cmd.Context(), "slack/connect", struct{}{}, &start); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Sign in with Slack to link your account to org %s:\n", m.Org)
			fmt.Fprintln(cmd.OutOrStdout(), start.URL)
			if !noOpen {
				if err := openBrowser(start.URL); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Open the link above yourself: %v\n", err)
				}
			}
			before, err := slackMemberAddedAt(cmd, c)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), slackConnectTimeout)
			defer cancel()
			for {
				list, err := slackMemberList(ctx, c)
				if err == nil {
					for _, p := range list {
						// Linked from this machine, and new or re-linked since we started.
						if p.AddedBy == m.Machine && (before[p.UserID].IsZero() || !p.AddedAt.Equal(before[p.UserID])) {
							fmt.Fprintf(cmd.OutOrStdout(), "Linked %s (%s) to org %s.\n", p.Name, p.UserID, m.Org)
							return nil
						}
					}
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("timed out after %s waiting for the Slack sign-in", slackConnectTimeout)
				case <-time.After(slackConnectEvery):
				}
			}
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the link without opening a browser")
	return cmd
}

func slackMemberList(ctx context.Context, c pool.Client) ([]slackMember, error) {
	var list []slackMember
	err := c.Post(ctx, "slack/members", map[string]string{"op": "list"}, &list)
	return list, err
}

// slackMemberAddedAt maps each linked user to when they were last linked.
func slackMemberAddedAt(cmd *cobra.Command, c pool.Client) (map[string]time.Time, error) {
	list, err := slackMemberList(cmd.Context(), c)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, p := range list {
		out[p.UserID] = p.AddedAt
	}
	return out, nil
}
