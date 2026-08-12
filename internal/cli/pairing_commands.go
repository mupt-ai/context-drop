package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/drop"
	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newInitCommand() *cobra.Command {
	var endpoint string
	var machineName string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a new machine chain on this endpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			resp, err := CreateChain(cmd.Context(), CreateChainRequest{
				Endpoint:    cfg.Endpoint,
				MachineName: effectiveMachineName(cfg, machineName),
			})
			if err != nil {
				return err
			}
			cfg.ChainID = resp.ChainID
			cfg.MachineID = resp.MachineID
			cfg.MachineName = resp.MachineName
			cfg.ChainSessionToken = resp.SessionToken
			if err := config.SaveCLIConfig(cfg); err != nil {
				return err
			}
			detected, err := runtimeclient.Initialize()
			if err != nil {
				return fmt.Errorf("initialize local runtime: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized chain %s as %s (%s)\n", cfg.ChainID, cfg.MachineName, cfg.MachineID)
			if len(detected) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: no supported local agent CLIs detected; pairing and handoffs remain available")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "detected local agents: %s\n", strings.Join(detected, ", "))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "start local orchestration with: context-drop daemon start")
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().StringVar(&machineName, "machine-name", "", "name for this machine in the chain")
	return cmd
}

func newLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove saved machine-chain credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			cfg.ChainID = ""
			cfg.MachineID = ""
			cfg.MachineName = ""
			cfg.ChainSessionToken = ""
			if err := config.SaveCLIConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "logged out")
			return nil
		},
	}
}

func newTokenCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage one-time join tokens"}
	cmd.AddCommand(newCreateJoinTokenCommand("create"))
	return cmd
}

func newCreateJoinTokenCommand(use string) *cobra.Command {
	var endpoint string
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   use,
		Short: "Create a single-use join token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			return printFreshJoinToken(cmd, &cfg, ttl)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "join token TTL, max 15m")
	return cmd
}

func newJoinCommand() *cobra.Command {
	var endpoint string
	var machineName string
	cmd := &cobra.Command{
		Use:   "join <token>",
		Short: "Join an existing machine chain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			resp, err := JoinChain(cmd.Context(), JoinChainRequest{
				Endpoint:    cfg.Endpoint,
				Token:       args[0],
				MachineName: effectiveMachineName(cfg, machineName),
			})
			if err != nil {
				return err
			}
			cfg.ChainID = resp.ChainID
			cfg.MachineID = resp.MachineID
			cfg.MachineName = resp.MachineName
			cfg.ChainSessionToken = resp.SessionToken
			if err := config.SaveCLIConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "joined chain %s as %s (%s)\n", cfg.ChainID, cfg.MachineName, cfg.MachineID)
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().StringVar(&machineName, "machine-name", "", "name for this machine in the chain")
	return cmd
}

func newMachinesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "machines", Short: "Manage machines in this chain"}
	cmd.AddCommand(newMachinesListCommand())
	return cmd
}

func newMachinesListCommand() *cobra.Command {
	var endpoint string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List machines in this chain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			resp, err := ListMachines(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
			}
			for _, machine := range resp.Machines {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", machine.ID, machine.Name, machine.LastSeenAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON response")
	return cmd
}

func newSendCommand() *cobra.Command {
	var endpoint string
	var to string
	cmd := &cobra.Command{
		Use:   "send --to <machine-id|name> <message-or-file>",
		Short: "Send a message or file drop to a machine in this chain",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			if strings.TrimSpace(to) == "" {
				return fmt.Errorf("--to is required")
			}
			body, uploaded, err := sendBody(cmd, cfg, args)
			if err != nil {
				return err
			}
			resp, err := SendMessage(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, to, body)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sent %s\n", resp.Message.ID)
			if uploaded.URL != "" {
				fmt.Fprintln(cmd.OutOrStdout(), uploaded.URL)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().StringVar(&to, "to", "", "target machine id or unique name")
	return cmd
}

func sendBody(cmd *cobra.Command, cfg config.CLIConfig, args []string) (string, UploadResponse, error) {
	if len(args) != 1 || !isRegularFile(args[0]) {
		return strings.Join(args, " "), UploadResponse{}, nil
	}
	data, filename, contentType, err := inputData(args, false)
	if err != nil {
		return "", UploadResponse{}, err
	}
	resp, err := Upload(cmd.Context(), UploadRequest{
		Endpoint:    cfg.Endpoint,
		UploadToken: cfg.UploadToken,
		Filename:    drop.SafeFilename(filename),
		ContentType: contentType,
		TTL:         cfg.DefaultTTL,
		Data:        data,
	})
	if err != nil {
		return "", UploadResponse{}, err
	}
	return fmt.Sprintf("drop %s %s %s", resp.ID, resp.URL, drop.SafeFilename(filename)), resp, nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func newMessagesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "messages", Short: "Read messages for this machine"}
	cmd.AddCommand(newMessagesListCommand())
	return cmd
}

func newMessagesListCommand() *cobra.Command {
	var endpoint string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List messages sent to this machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			resp, err := ListMessages(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
			}
			for _, msg := range resp.Messages {
				body := strings.ReplaceAll(msg.Body, "\n", "\\n")
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", msg.CreatedAt.Format(time.RFC3339), msg.FromMachineID, body)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON response")
	return cmd
}

func printFreshJoinToken(cmd *cobra.Command, cfg *config.CLIConfig, ttl time.Duration) error {
	resp, err := CreateInvite(cmd.Context(), CreateInviteRequest{
		Endpoint:          cfg.Endpoint,
		ChainSessionToken: cfg.ChainSessionToken,
		TTL:               ttl,
	})
	if err != nil {
		return err
	}
	if resp.ChainID != "" {
		cfg.ChainID = resp.ChainID
	}
	if resp.MachineID != "" {
		cfg.MachineID = resp.MachineID
	}
	if resp.MachineName != "" {
		cfg.MachineName = resp.MachineName
	}
	if resp.SessionToken != "" {
		cfg.ChainSessionToken = resp.SessionToken
	}
	if err := config.SaveCLIConfig(*cfg); err != nil {
		return err
	}
	printJoinToken(cmd, resp)
	return nil
}

func effectiveMachineName(cfg config.CLIConfig, override string) string {
	if name := strings.TrimSpace(override); name != "" {
		return name
	}
	if name := strings.TrimSpace(cfg.MachineName); name != "" {
		return name
	}
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "machine"
	}
	return name
}

func printJoinToken(cmd *cobra.Command, resp CreateInviteResponse) {
	fmt.Fprintf(cmd.OutOrStdout(), "join token: %s\n", resp.Token)
	fmt.Fprintf(cmd.ErrOrStderr(), "join token expires at %s\n", resp.ExpiresAt.Format(time.RFC3339))
}
