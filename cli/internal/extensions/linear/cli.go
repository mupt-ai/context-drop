package linear

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// Command is `context-drop linear`.
func Command(env extensions.CLI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "linear",
		Short: "Connect the org to Linear, so agents can comment on issues and people can answer",
		Long: "Once the org is connected, `send linear:ENG-123 \"...\"` comments on an issue. A later comment on that\n" +
			"issue comes back to the agent as its next prompt. Read an issue's comments with `linear read`.",
	}
	cmd.AddCommand(installCommand(env), statusCommand(env), readCommand(env), uninstallCommand(env))
	return cmd
}

func installCommand(env extensions.CLI) *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Connect the org to Linear: approve the app in your browser",
		Long: "Opens Linear so a workspace admin can approve Context Drop; agents' comments then appear as the app.\n" +
			"On a server without a Linear app, paste a workspace admin's personal API key instead (Settings →\n" +
			"Security & access → Personal API keys), or pass it with --key or $LINEAR_API_KEY.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			if key == "" {
				key = os.Getenv("LINEAR_API_KEY")
			}
			if key == "" {
				// With a Linear app on the server, an admin approves it in the browser.
				var start struct{ URL string }
				err := c.Post(cmd.Context(), "linear/install", map[string]string{}, &start)
				if err == nil && start.URL != "" {
					return approveInBrowser(cmd, c, m.Org, start.URL)
				}
				if key, err = readKey(cmd); err != nil {
					return err
				}
			}
			var out struct{ Workspace, User string }
			if err := c.Post(cmd.Context(), "linear/install", map[string]string{"key": key}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Org %s is connected to Linear workspace %s. Agents comment as %s.\n", m.Org, out.Workspace, out.User)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "the Linear API key (default: $LINEAR_API_KEY, or a prompt)")
	return cmd
}

// readKey prompts for the API key, without echoing it when the input is a terminal.
func readKey(cmd *cobra.Command) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), "Linear API key: ")
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no API key given")
	}
	return strings.TrimSpace(line), nil
}

func statusCommand(env extensions.CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the org's Linear workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			var st Status
			if err := c.Post(cmd.Context(), "linear/status", struct{}{}, &st); err != nil {
				return err
			}
			if !st.Installed {
				fmt.Fprintf(cmd.OutOrStdout(), "Org %s is not connected to Linear. Run `context-drop linear install`.\n", m.Org)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Org %s is connected to Linear workspace %s. Agents comment as %s.\n", m.Org, st.Workspace, st.User)
			return nil
		},
	}
}

func readCommand(env extensions.CLI) *cobra.Command {
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "read ISSUE",
		Short: "Read an issue's latest comments, oldest first",
		Long:  "ISSUE is an identifier like ENG-123, with or without linear:.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := env.Client()
			if err != nil {
				return err
			}
			target := args[0]
			if !strings.HasPrefix(target, Prefix) {
				target = Prefix + target
			}
			var res ReadResult
			if err := c.Post(cmd.Context(), "linear/read", ReadRequest{Target: target, Limit: limit}, &res); err != nil {
				return err
			}
			if asJSON {
				return env.JSON(cmd.OutOrStdout(), res)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s %s\n%s\n", res.Issue.Identifier, res.Issue.Title, res.Issue.URL)
			for _, cm := range res.Comments {
				who := cm.User
				if who == "" {
					who = "someone"
				}
				fmt.Fprintf(w, "── %s · %s\n%s\n", who, cm.CreatedAt.Local().Format("Jan 2 15:04"), cm.Body)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "at most this many comments (up to 100)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func uninstallCommand(env extensions.CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Disconnect the org from Linear and remove its webhook",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, m, err := env.Client()
			if err != nil {
				return err
			}
			if err := c.Post(cmd.Context(), "linear/uninstall", struct{}{}, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Org %s is disconnected from Linear. Revoke the API key in Linear too.\n", m.Org)
			return nil
		},
	}
}

// installWait is how long `linear install` waits for the browser approval, and how often it checks.
var installWait, installEvery = 10 * time.Minute, 2 * time.Second

// approveInBrowser opens Linear's approval page and waits until the org shows as connected.
func approveInBrowser(cmd *cobra.Command, c pool.Client, org, link string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "Approve Context Drop in Linear to connect org %s:\n", org)
	fmt.Fprintln(cmd.OutOrStdout(), link)
	if err := extensions.OpenBrowser(link); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Open the link above yourself: %v\n", err)
	}
	var before Status
	_ = c.Post(cmd.Context(), "linear/status", struct{}{}, &before)
	ctx, cancel := context.WithTimeout(cmd.Context(), installWait)
	defer cancel()
	for {
		var st Status
		if err := c.Post(ctx, "linear/status", struct{}{}, &st); err == nil && st.Installed && st.Since != before.Since {
			fmt.Fprintf(cmd.OutOrStdout(), "Org %s is connected to Linear workspace %s.\n", org, st.Workspace)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for the Linear approval", installWait)
		case <-time.After(installEvery):
		}
	}
}
