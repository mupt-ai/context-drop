package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newRepoCommand() *cobra.Command {
	root := &cobra.Command{Use: "repo", Short: "Manage validated repository aliases for agent launches"}
	var jsonOut bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := runtimeclient.LoadConfig()
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"aliases": cfg.RepoAliases})
		}
		aliases := make([]string, 0, len(cfg.RepoAliases))
		for alias := range cfg.RepoAliases {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", alias, cfg.RepoAliases[alias])
		}
		return nil
	}}
	list.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	add := &cobra.Command{Use: "add <alias> <absolute-path>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := runtimeclient.ConfigureRepoAlias(args[0], args[1], false); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "saved repository alias %s\n", args[0])
		return nil
	}}
	remove := &cobra.Command{Use: "remove <alias>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := runtimeclient.ConfigureRepoAlias(args[0], "", true); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed repository alias %s\n", args[0])
		return nil
	}}
	root.AddCommand(list, add, remove)
	return root
}
