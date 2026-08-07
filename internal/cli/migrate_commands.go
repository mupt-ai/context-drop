package cli

import (
	"encoding/json"
	"fmt"

	"contextdrop.dev/context-drop/internal/migration"
	"github.com/spf13/cobra"
)

func newMigrateCommand() *cobra.Command {
	root := &cobra.Command{Use: "migrate", Short: "Inspect legacy installations for safe migration"}
	relaymux := &cobra.Command{Use: "relaymux", Short: "Inspect a Relaymux installation"}
	var home string
	var jsonOut bool
	inspect := &cobra.Command{Use: "inspect", Short: "Inventory Relaymux state without modifying it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		inventory, err := migration.InspectRelaymux(home)
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(inventory)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Relaymux home: %s\nSchedules: %d\nLegacy runs: %d\nLegacy events: %d\n", inventory.Home, len(inventory.Schedules), inventory.Counts.Runs, inventory.Counts.Events)
		for _, blocker := range inventory.Blockers {
			fmt.Fprintf(cmd.OutOrStdout(), "Unsupported: %s\n", blocker)
		}
		return nil
	}}
	inspect.Flags().StringVar(&home, "home", "", "legacy Relaymux home directory")
	inspect.Flags().BoolVar(&jsonOut, "json", false, "print JSON inventory")
	_ = inspect.MarkFlagRequired("home")
	relaymux.AddCommand(inspect)
	root.AddCommand(relaymux)
	return root
}
