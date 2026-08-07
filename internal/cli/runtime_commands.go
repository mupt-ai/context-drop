package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newAgentCommand() *cobra.Command {
	root := &cobra.Command{Use: "agent", Short: "Manage local agent registrations"}
	var jsonOut bool
	list := &cobra.Command{Use: "list", Short: "List configured local agent CLIs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := runtimeclient.New()
		if err != nil {
			return err
		}
		agents, err := client.Agents(cmd.Context())
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"agents": agents})
		}
		for _, a := range agents {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", a.Name, a.Command, a.PromptMode)
		}
		return nil
	}}
	list.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	root.AddCommand(list)
	return root
}

func newLaunchCommand() *cobra.Command {
	var agent, repo, prompt, name, backend, workspace string
	var jsonOut bool
	cmd := &cobra.Command{Use: "launch", Short: "Launch a local agent in a visible tmux window or Herdr workspace", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(repo) == "" || strings.TrimSpace(prompt) == "" || strings.TrimSpace(agent) == "" {
			return fmt.Errorf("--agent, --repo, and --prompt are required")
		}
		client, err := runtimeclient.New()
		if err != nil {
			return err
		}
		if backend != "" && backend != "tmux" && backend != "herdr" {
			return fmt.Errorf("--backend must be tmux or herdr")
		}
		if workspace != "" {
			effectiveBackend := backend
			if effectiveBackend == "" {
				cfg, configErr := runtimeclient.LoadConfig()
				if configErr != nil {
					return configErr
				}
				effectiveBackend = cfg.DefaultBackend
			}
			if effectiveBackend != "herdr" {
				return fmt.Errorf("--workspace requires the herdr backend")
			}
		}
		run, err := client.Launch(cmd.Context(), agent, repo, prompt, name, backend, workspace)
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(run)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", run.ID, run.Status, run.Backend)
		if run.Backend == "herdr" {
			fmt.Fprintf(cmd.OutOrStdout(), "herdr --session %s workspace focus %s\nherdr session attach %s\n", run.HerdrSession, run.HerdrWorkspace, run.HerdrSession)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "tmux attach -t %s\n", run.TmuxSession)
		}
		return nil
	}}
	cmd.Flags().StringVar(&agent, "agent", "", "configured local agent name")
	cmd.Flags().StringVar(&repo, "repo", "", "local repository path")
	cmd.Flags().StringVar(&prompt, "prompt", "", "task prompt")
	cmd.Flags().StringVar(&name, "name", "", "tmux window or Herdr workspace name")
	cmd.Flags().StringVar(&backend, "backend", "", "session backend: tmux or herdr (default from runtime config)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "existing Herdr workspace ID to use (creates a new tab inside it)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func newRunCommand() *cobra.Command {
	root := &cobra.Command{Use: "run", Short: "Inspect local agent runs"}
	var jsonOut bool
	list := &cobra.Command{Use: "list", Short: "List local runs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, e := runtimeclient.New()
		if e != nil {
			return e
		}
		runs, e := c.Runs(cmd.Context())
		if e != nil {
			return e
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"runs": runs})
		}
		for _, r := range runs {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Status, r.Backend, r.Agent, r.Name)
		}
		return nil
	}}
	show := &cobra.Command{Use: "show <id>", Short: "Show one local run", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, e := runtimeclient.New()
		if e != nil {
			return e
		}
		r, e := c.Run(cmd.Context(), args[0])
		if e != nil {
			return e
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}}
	list.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	root.AddCommand(list, show)
	return root
}

func newRuntimeCommand() *cobra.Command {
	root := &cobra.Command{Use: "runtime", Short: "Manage the private local agent runtime"}
	serve := &cobra.Command{Use: "serve", Short: "Run the loopback-only local runtime", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if _, err := runtimeclient.Initialize(); err != nil {
			return err
		}
		entry, err := runtimeclient.RuntimeEntry()
		if err != nil {
			return err
		}
		_, configPath, _, err := runtimeclient.Paths()
		if err != nil {
			return err
		}
		process := exec.CommandContext(cmd.Context(), "node", entry, configPath)
		process.Stdout = cmd.OutOrStdout()
		process.Stderr = cmd.ErrOrStderr()
		process.Stdin = os.Stdin
		return process.Run()
	}}
	status := &cobra.Command{Use: "status", Short: "Check local runtime health", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, e := runtimeclient.New()
		if e != nil {
			return e
		}
		if e = c.Health(cmd.Context()); e != nil {
			return e
		}
		fmt.Fprintln(cmd.OutOrStdout(), "ok")
		return nil
	}}
	root.AddCommand(serve, status)
	return root
}
