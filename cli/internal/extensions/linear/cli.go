package linear

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"contextdrop.dev/context-drop/internal/extensions"
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
		Short: "Connect the org to Linear with a workspace admin's API key",
		Long: "Create a personal API key in Linear (Settings → Security & access → Personal API keys) as a workspace\n" +
			"admin, then run this and paste it. Agents' comments appear under that key's user. The key is also read\n" +
			"from --key or $LINEAR_API_KEY.",
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
