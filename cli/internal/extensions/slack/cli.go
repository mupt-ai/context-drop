package slack

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// Send posts a message to people on the org's Slack from the CLI. When an agent sends it, replies in
// the thread (or DM) come back to that agent as prompts; wait also prints the first replies here.
func Send(cmd *cobra.Command, env extensions.CLI, target, text, from string, wait bool, timeout time.Duration) error {
	c, _, err := env.Client()
	if err != nil {
		return err
	}
	out, err := c.Send(cmd.Context(), pool.SendRequest{To: target, Text: text, From: from, Agent: env.Agent(cmd.Context())})
	if err != nil {
		return err
	}
	if len(out.Results) == 0 {
		return fmt.Errorf("Slack returned no result")
	}
	ref := out.Results[0].ID
	fmt.Fprintf(cmd.ErrOrStderr(), "sent to %s via slack\n", ref)
	fmt.Fprintln(cmd.OutOrStdout(), ref)
	if !wait {
		return nil
	}
	// Replies are the messages after ours, by Slack's own clock rather than this machine's.
	since := out.Results[0].TS
	if since == "" {
		_, since, _ = strings.Cut(strings.TrimPrefix(ref, "slack:"), "/")
	}
	return waitForReply(cmd, c, ref, since, timeout)
}

// waitEvery is how often waitForReply reads Slack.
var waitEvery = 3 * time.Second

// waitForReply reads a thread (and, for a DM, the DM itself) until a person replies, and prints the replies.
func waitForReply(cmd *cobra.Command, c pool.Client, ref, since string, timeout time.Duration) error {
	targets := []string{ref}
	if ch, _, _ := strings.Cut(strings.TrimPrefix(ref, "slack:"), "/"); strings.HasPrefix(ch, "D") {
		targets = append(targets, "slack:"+ch)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	for {
		seen := map[string]bool{}
		var replies []pool.SlackMessage
		for _, t := range targets {
			res, err := c.SlackRead(ctx, pool.SlackReadRequest{Target: t, Oldest: since})
			if err != nil {
				continue
			}
			for _, msg := range res.Messages {
				if !msg.Bot && !seen[msg.TS] && after(msg.TS, since) {
					seen[msg.TS] = true
					replies = append(replies, msg)
				}
			}
		}
		if len(replies) > 0 {
			printMessages(cmd, replies)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for a reply on %s", timeout, ref)
		case <-time.After(waitEvery):
		}
	}
}

// after reports whether Slack timestamp ts is later than since; an empty since accepts everything.
func after(ts, since string) bool {
	if since == "" {
		return true
	}
	a, errA := strconv.ParseFloat(ts, 64)
	b, errB := strconv.ParseFloat(since, 64)
	if errA != nil || errB != nil {
		return ts > since
	}
	return a > b
}

func printMessages(cmd *cobra.Command, msgs []pool.SlackMessage) {
	w := cmd.OutOrStdout()
	for _, msg := range msgs {
		who := msg.Name
		if who == "" {
			who = msg.User
		}
		if who == "" {
			who = "bot"
		}
		if msg.Bot {
			who += " (app)"
		}
		when := ""
		if f, err := strconv.ParseFloat(msg.TS, 64); err == nil {
			when = " · " + time.Unix(int64(f), 0).Format("Jan 2 15:04")
		}
		thread := ""
		if msg.Replies > 0 {
			thread = fmt.Sprintf(" · %d replies", msg.Replies)
		}
		fmt.Fprintf(w, "── %s%s%s · %s\n%s\n", who, when, thread, msg.Ref, msg.Text)
	}
}

// Command is `context-drop slack`.
func Command(env extensions.CLI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slack",
		Short: "Connect the org to Slack, so agents can message people and people can answer",
		Long: "Once the org is connected, `send` reaches people as well as agents: `#channel`, `@person`, or a\n" +
			"`slack:CHANNEL/TS` thread. A reply in a thread an agent started, or in a DM an agent opened, comes back\n" +
			"to that agent as its next prompt. Other channel messages are never pushed; read them with `slack read`.\n" +
			"Only people linked with `slack allow` can reach agents.",
	}
	cmd.AddCommand(installCommand(env), connectCommand(env), statusCommand(env), allowCommand(env), disallowCommand(env),
		readCommand(env), uninstallCommand(env), manifestCommand(env))
	return cmd
}

func installCommand(env extensions.CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Print a link that connects the org to a Slack workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			var out struct{ URL string }
			if err := c.Post(cmd.Context(), "slack/install", struct{}{}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Connecting org %s to Slack in your browser (the link expires in 30 minutes):\n", m.Org)
			fmt.Fprintln(cmd.OutOrStdout(), out.URL)
			if err := extensions.OpenBrowser(out.URL); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Open the link above yourself: %v\n", err)
			}
			return nil
		},
	}
}

type memberRow struct {
	UserID  string    `json:"user_id"`
	Name    string    `json:"name"`
	AddedBy string    `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

func statusCommand(env extensions.CLI) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the org's Slack workspace and the people linked to it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			var st struct {
				Configured bool        `json:"configured"`
				Installed  bool        `json:"installed"`
				Team       string      `json:"team"`
				TeamID     string      `json:"team_id"`
				BotUserID  string      `json:"bot_user_id"`
				Members    []memberRow `json:"members"`
			}
			if err := c.Post(cmd.Context(), "slack/status", struct{}{}, &st); err != nil {
				return err
			}
			if asJSON {
				return env.JSON(cmd.OutOrStdout(), st)
			}
			out := cmd.OutOrStdout()
			switch {
			case !st.Configured:
				fmt.Fprintf(out, "Slack is not configured on %s.\n", m.Server)
				return nil
			case !st.Installed:
				fmt.Fprintf(out, "Org %s is not connected to Slack. Run `context-drop slack install`.\n", m.Org)
				return nil
			}
			fmt.Fprintf(out, "Org %s is connected to Slack workspace %s (%s).\n", m.Org, st.Team, st.TeamID)
			if len(st.Members) == 0 {
				fmt.Fprintln(out, "Nobody is linked yet, so no one can answer agents. Run `context-drop slack allow @person`.")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "\nPERSON\tSLACK ID\tADDED BY\tADDED")
			for _, p := range st.Members {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.UserID, p.AddedBy, p.AddedAt.Format("Jan 2 2006"))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func members(cmd *cobra.Command, env extensions.CLI, op, user string) (memberRow, error) {
	var out memberRow
	c, _, err := env.Client()
	if err != nil {
		return out, err
	}
	return out, c.Post(cmd.Context(), "slack/members", map[string]string{"op": op, "user": user}, &out)
}

func allowCommand(env extensions.CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "allow @PERSON...",
		Short: "Let people answer agents: their DMs and thread replies reach the agents waiting on them",
		Long:  "PERSON is a Slack handle, display name, email, or user ID. Messages from people who are not linked never reach an agent.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, who := range args {
				p, err := members(cmd, env, "add", who)
				if err != nil {
					return fmt.Errorf("%s: %w", who, err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "linked %s (%s)\n", p.Name, p.UserID)
			}
			return nil
		},
	}
}

func disallowCommand(env extensions.CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "disallow @PERSON...",
		Short: "Stop people's messages from reaching agents",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, who := range args {
				p, err := members(cmd, env, "remove", who)
				if err != nil {
					return fmt.Errorf("%s: %w", who, err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "unlinked %s (%s)\n", p.Name, p.UserID)
			}
			return nil
		},
	}
}

func readCommand(env extensions.CLI) *cobra.Command {
	var limit int
	var oldest string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "read TARGET",
		Short: "Read a Slack channel, a DM with the app, or a thread, oldest first",
		Long:  "TARGET is #channel, @person (their DM with the app), or slack:CHANNEL/TS for a thread.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := env.Client()
			if err != nil {
				return err
			}
			res, err := c.SlackRead(cmd.Context(), pool.SlackReadRequest{Target: args[0], Limit: limit, Oldest: oldest})
			if err != nil {
				return err
			}
			if asJSON {
				return env.JSON(cmd.OutOrStdout(), res)
			}
			printMessages(cmd, res.Messages)
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "at most this many messages (up to 200)")
	cmd.Flags().StringVar(&oldest, "oldest", "", "only messages after this Slack timestamp")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func uninstallCommand(env extensions.CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Disconnect the org from Slack and forget its linked people",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			if err := c.Post(cmd.Context(), "slack/uninstall", struct{}{}, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Org %s is disconnected from Slack. Remove the app in Slack too to revoke its token.\n", m.Org)
			return nil
		},
	}
}

func manifestCommand(env extensions.CLI) *cobra.Command {
	var server, name string
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Print the Slack app manifest for a Context Drop server",
		Long: "Create the app at https://api.slack.com/apps → Create New App → From a manifest, then give the server its\n" +
			"client ID, client secret, and signing secret (see docs/server.md#slack).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if server == "" {
				server = env.Server()
			}
			fmt.Fprint(cmd.OutOrStdout(), Manifest(name, server))
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "the server's public URL (default: this machine's org server)")
	cmd.Flags().StringVar(&name, "name", "Context Drop", "the app's name in Slack")
	return cmd
}
