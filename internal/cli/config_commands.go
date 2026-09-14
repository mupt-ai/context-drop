package cli

import (
	"fmt"
	"strings"

	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "Inspect and change local runtime configuration"}
	workerAgent := &cobra.Command{
		Use:   "worker-agent [" + strings.Join(runtimeclient.WorkerAgents, "|") + "]",
		Short: "Show or set which agent the four pool workers run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := runtimeclient.SetWorkerAgent(args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "worker agent set to %s; apply it with: context-drop daemon restart\n", args[0])
				return nil
			}
			cfg, err := runtimeclient.LoadConfig()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "worker agent: %s\n", cfg.WorkerAgent)
			for _, name := range runtimeclient.ConfiguredAgents(cfg) {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", name, strings.Join(cfg.Agents[name].Command, " "))
			}
			return nil
		},
	}
	root.AddCommand(workerAgent)
	return root
}
