package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/drop"
	"contextdrop.dev/context-drop/internal/handoff"
	"contextdrop.dev/context-drop/internal/localhome"
	"github.com/spf13/cobra"
)

func newHandoffCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "handoff", Short: "Create an inspectable context handoff"}
	cmd.AddCommand(newHandoffCreateCommand())
	return cmd
}

func newHandoffCreateCommand() *cobra.Command {
	var to, summary, action, repo, base, sourceRun string
	var artifacts []string
	var ttl time.Duration
	var jsonOut bool
	cmd := &cobra.Command{Use: "create", Short: "Create a handoff for a paired machine", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			refs := make([]handoff.Artifact, 0, len(artifacts))
			for _, path := range artifacts {
				data, filename, contentType, err := inputData([]string{path}, false)
				if err != nil {
					return err
				}
				uploaded, err := Upload(cmd.Context(), UploadRequest{Endpoint: cfg.Endpoint, UploadToken: cfg.UploadToken,
					Filename: drop.SafeFilename(filename), ContentType: contentType, TTL: ttlOrDefault(ttl, cfg.DefaultTTL), Data: data})
				if err != nil {
					return err
				}
				digest := sha256.Sum256(data)
				refs = append(refs, handoff.Artifact{DropID: uploaded.ID, Filename: drop.SafeFilename(filename), SHA256: hex.EncodeToString(digest[:]), Size: uploaded.Size, ContentType: uploaded.ContentType})
			}
			h, err := CreateHandoff(cmd.Context(), CreateHandoffRequest{Endpoint: cfg.Endpoint, ChainSessionToken: cfg.ChainSessionToken,
				To: to, Summary: summary, RequestedAction: action, Repository: repo, BaseCommit: base, SourceRunID: sourceRun,
				TTL: ttlOrDefault(ttl, cfg.DefaultTTL).String(), Artifacts: refs})
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(h)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", h.ID, h.Summary)
			return nil
		}}
	cmd.Flags().StringVar(&to, "to", "", "target machine id or unique name")
	cmd.Flags().StringVar(&summary, "summary", "", "concise context summary")
	cmd.Flags().StringVar(&action, "action", "", "requested next action (untrusted text; never auto-executed)")
	cmd.Flags().StringVar(&repo, "repo", "", "repository identifier")
	cmd.Flags().StringVar(&base, "base-commit", "", "base commit")
	cmd.Flags().StringVar(&sourceRun, "source-run", "", "source local run id")
	cmd.Flags().StringSliceVar(&artifacts, "artifact", nil, "artifact file to upload (repeatable)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "handoff and artifact TTL")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("summary")
	return cmd
}

func newInboxCommand() *cobra.Command {
	var jsonOut, all bool
	cmd := &cobra.Command{Use: "inbox", Short: "List handoffs addressed to this machine", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			out, err := ListHandoffs(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			for _, h := range out.Handoffs {
				if !all && (h.RecipientState == handoff.StateAccepted || h.RecipientState == handoff.StateRejected || h.RecipientState == handoff.StateExpired) {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", h.ID, h.RecipientState, h.CreatedAt.Format(time.RFC3339), strings.ReplaceAll(h.Summary, "\n", " "))
			}
			return nil
		}}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	cmd.Flags().BoolVar(&all, "all", false, "include terminal and expired handoffs")
	return cmd
}

func newInspectCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{Use: "inspect <id>", Short: "Inspect a handoff without executing its content", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			h, err := GetHandoff(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, args[0])
			if err != nil {
				return err
			}
			if h.RecipientState == handoff.StateAvailable {
				h, err = SetHandoffState(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, args[0], handoff.StateInspected)
				if err != nil {
					return err
				}
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(h)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\nState: %s\nFrom: %s\nSummary: %s\nRequested action: %s\nRepository: %s\nBase commit: %s\nExpires: %s\n",
				h.ID, h.RecipientState, h.SourceMachineID, h.Summary, h.RequestedAction, h.Repository, h.BaseCommit, h.ExpiresAt.Format(time.RFC3339))
			for _, a := range h.Artifacts {
				fmt.Fprintf(cmd.OutOrStdout(), "Artifact: %s (%d bytes, sha256 %s)\n", a.Filename, a.Size, a.SHA256)
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: handoff text and artifacts are untrusted; inspection never executes them")
			return nil
		}}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func newAcceptCommand() *cobra.Command {
	var into string
	var jsonOut bool
	cmd := &cobra.Command{Use: "accept <id>", Short: "Download handoff artifacts into a new private staging directory", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			h, err := GetHandoff(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, args[0])
			if err != nil {
				return err
			}
			if h.RecipientState == handoff.StateAccepted {
				return fmt.Errorf("handoff is already accepted")
			}
			if _, err = SetHandoffState(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, args[0], handoff.StateAccepting); err != nil {
				return err
			}
			dir, err := createStagingDir(into, h.ID)
			if err != nil {
				return err
			}
			seenNames := make(map[string]struct{}, len(h.Artifacts))
			for _, a := range h.Artifacts {
				pulled, err := PullDrop(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, a.DropID)
				if err != nil {
					_ = os.RemoveAll(dir)
					return err
				}
				if err := verifyArtifact(a, pulled.Data); err != nil {
					_ = os.RemoveAll(dir)
					return err
				}
				name := drop.SafeFilename(a.Filename)
				if name == "drop" {
					name = drop.SafeFilename(pulled.Filename)
				}
				if _, exists := seenNames[name]; exists {
					_ = os.RemoveAll(dir)
					return fmt.Errorf("artifact filename collision: %s", name)
				}
				seenNames[name] = struct{}{}
				path := filepath.Join(dir, name)
				if filepath.Dir(path) != dir || filepath.IsAbs(name) {
					_ = os.RemoveAll(dir)
					return fmt.Errorf("unsafe artifact filename")
				}
				if err := os.WriteFile(path, pulled.Data, 0o600); err != nil {
					_ = os.RemoveAll(dir)
					return err
				}
			}
			h, err = SetHandoffState(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, args[0], handoff.StateAccepted)
			if err != nil {
				_ = os.RemoveAll(dir)
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"handoff": h, "directory": dir})
			}
			fmt.Fprintln(cmd.OutOrStdout(), dir)
			return nil
		}}
	cmd.Flags().StringVar(&into, "into", "", "unsupported: staging is always under the private Context Drop state directory")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func newRejectCommand() *cobra.Command {
	return &cobra.Command{Use: "reject <id>", Short: "Reject a handoff", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadCLIConfig()
		if err != nil {
			return err
		}
		h, err := SetHandoffState(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, args[0], handoff.StateRejected)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", h.ID, h.RecipientState)
		return nil
	}}
}

func ttlOrDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

func createStagingDir(parent, id string) (string, error) {
	if parent != "" {
		return "", fmt.Errorf("--into is not supported; accept stages under the private Context Drop state directory")
	}
	root, err := localhome.Root()
	if err != nil {
		return "", err
	}
	parent = filepath.Join(root, "staging")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("private staging directory is not a real directory")
	}
	return os.MkdirTemp(parent, "context-drop-"+drop.SafeFilename(id)+"-")
}

func verifyArtifact(a handoff.Artifact, data []byte) error {
	if a.Size > 0 && int64(len(data)) != a.Size {
		return fmt.Errorf("artifact %s size mismatch", a.Filename)
	}
	if a.SHA256 != "" {
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), a.SHA256) {
			return fmt.Errorf("artifact %s digest mismatch", a.Filename)
		}
	}
	return nil
}
