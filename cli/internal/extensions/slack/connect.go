package slack

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// connectTimeout is how long `slack connect` waits for the sign-in to finish.
var connectTimeout = 10 * time.Minute

// connectEvery is how often `slack connect` checks whether the sign-in finished.
var connectEvery = 2 * time.Second

func connectCommand(env extensions.CLI) *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Link your own Slack account to this org, so agents can reach you on Slack",
		Long: "Opens Slack in your browser. Sign in, and your Slack account is linked to the org: agents can message you,\n" +
			"and your DMs and thread replies reach them. Only you can link yourself this way; `slack allow` links someone else.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			var start struct{ URL string }
			if err := c.Post(cmd.Context(), "slack/connect", struct{}{}, &start); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Sign in with Slack to link your account to org %s:\n", m.Org)
			fmt.Fprintln(cmd.OutOrStdout(), start.URL)
			if !noOpen {
				if err := extensions.OpenBrowser(start.URL); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Open the link above yourself: %v\n", err)
				}
			}
			before, err := memberAddedAt(cmd, c)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), connectTimeout)
			defer cancel()
			for {
				list, err := memberList(ctx, c)
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
					return fmt.Errorf("timed out after %s waiting for the Slack sign-in", connectTimeout)
				case <-time.After(connectEvery):
				}
			}
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the link without opening a browser")
	return cmd
}

func memberList(ctx context.Context, c pool.Client) ([]memberRow, error) {
	var list []memberRow
	err := c.Post(ctx, "slack/members", map[string]string{"op": "list"}, &list)
	return list, err
}

// memberAddedAt maps each linked user to when they were last linked.
func memberAddedAt(cmd *cobra.Command, c pool.Client) (map[string]time.Time, error) {
	list, err := memberList(cmd.Context(), c)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, p := range list {
		out[p.UserID] = p.AddedAt
	}
	return out, nil
}
