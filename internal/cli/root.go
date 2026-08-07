package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/clipboard"
	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/drop"
	"github.com/spf13/cobra"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

type options struct {
	endpoint    string
	ttl         time.Duration
	filename    string
	contentType string
	clipboard   bool
	noClipboard bool
	noCopy      bool
	json        bool
}

func NewRootCommand(build BuildInfo) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:           "context-drop [path]",
		Short:         "Hand off context and run local agents across your machines",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpload(cmd.Context(), cmd, opts, args)
		},
	}

	addUploadFlags(cmd, opts)
	cmd.AddCommand(newInitCommand())
	cmd.AddCommand(newLogoutCommand())
	cmd.AddCommand(newTokenCommand())
	cmd.AddCommand(newJoinCommand())
	cmd.AddCommand(newMachinesCommand())
	cmd.AddCommand(newSendCommand())
	cmd.AddCommand(newMessagesCommand())
	cmd.AddCommand(newHandoffCommand())
	cmd.AddCommand(newInboxCommand())
	cmd.AddCommand(newInspectCommand())
	cmd.AddCommand(newAcceptCommand())
	cmd.AddCommand(newRejectCommand())
	cmd.AddCommand(newAgentCommand())
	cmd.AddCommand(newLaunchCommand())
	cmd.AddCommand(newRunCommand())
	cmd.AddCommand(newRuntimeCommand())
	cmd.AddCommand(newDaemonCommand())
	cmd.AddCommand(newScheduleCommand())
	cmd.AddCommand(newMigrateCommand())
	cmd.AddCommand(newIMessageCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newPullCommand())
	cmd.AddCommand(newUploadCommand(opts))
	cmd.AddCommand(newConfigCommand())
	cmd.AddCommand(newDoctorCommand())
	cmd.AddCommand(newVersionCommand(build))
	return cmd
}

func Execute(build BuildInfo) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewRootCommand(build)
	cmd.SetContext(ctx)
	return cmd.Execute()
}

func newUploadCommand(parent *options) *cobra.Command {
	opts := *parent
	cmd := &cobra.Command{
		Use:   "upload [path]",
		Short: "Upload a file or, with --clipboard, current clipboard image",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpload(cmd.Context(), cmd, &opts, args)
		},
	}
	addUploadFlags(cmd, &opts)
	return cmd
}

func addUploadFlags(cmd *cobra.Command, opts *options) {
	cmd.Flags().StringVar(&opts.endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().DurationVar(&opts.ttl, "ttl", 0, "link TTL, e.g. 1h, 24h")
	cmd.Flags().StringVar(&opts.filename, "filename", "", "override uploaded filename")
	cmd.Flags().StringVar(&opts.contentType, "content-type", "", "override content type")
	cmd.Flags().BoolVar(&opts.clipboard, "clipboard", false, "enable clipboard integration (copy output URL; with no path, upload current clipboard image)")
	cmd.Flags().BoolVar(&opts.noClipboard, "no-clipboard", false, "disable clipboard integration for this run, overriding config")
	cmd.Flags().BoolVar(&opts.noCopy, "no-copy", false, "deprecated alias for --no-clipboard")
	_ = cmd.Flags().MarkHidden("no-copy")
	cmd.MarkFlagsMutuallyExclusive("clipboard", "no-clipboard")
	cmd.Flags().BoolVar(&opts.json, "json", false, "print JSON response")
}

func runUpload(ctx context.Context, cmd *cobra.Command, opts *options, args []string) error {
	cfg, err := config.LoadCLIConfig()
	if err != nil {
		return err
	}
	if opts.endpoint != "" {
		cfg.Endpoint = opts.endpoint
	}
	if opts.ttl > 0 {
		cfg.DefaultTTL = opts.ttl
	}
	clipboardEnabled, err := effectiveClipboard(opts, cfg)
	if err != nil {
		return err
	}
	if cfg.ChainSessionToken == "" {
		if link, ok := linkOnlyInput(args); ok {
			return writeLinkOnly(cmd, clipboardEnabled, opts.json, link)
		}
		return errNotInitialized()
	}

	data, filename, contentType, err := inputData(args, clipboardEnabled)
	if err != nil {
		return err
	}
	if opts.filename != "" {
		filename = opts.filename
	}
	if opts.contentType != "" {
		contentType = opts.contentType
	}

	resp, err := Upload(ctx, UploadRequest{
		Endpoint:          cfg.Endpoint,
		ChainSessionToken: cfg.ChainSessionToken,
		Filename:          drop.SafeFilename(filename),
		ContentType:       contentType,
		TTL:               cfg.DefaultTTL,
		Data:              data,
	})
	if err != nil {
		return err
	}

	if opts.json {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(resp); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), resp.URL)
	}
	copyURLToClipboardIfRequested(cmd, clipboardEnabled, resp.URL)
	return nil
}

type linkOnlyResponse struct {
	URL string `json:"url"`
}

func linkOnlyInput(args []string) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	raw := strings.TrimSpace(args[0])
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	return raw, true
}

func writeLinkOnly(cmd *cobra.Command, clipboardEnabled bool, jsonOut bool, link string) error {
	fmt.Fprintln(cmd.ErrOrStderr(), "warning: not initialized or joined; using the link as-is. Run context-drop init to upload files/images.")
	if jsonOut {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(linkOnlyResponse{URL: link}); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), link)
	}
	copyURLToClipboardIfRequested(cmd, clipboardEnabled, link)
	return nil
}

func effectiveClipboard(opts *options, cfg config.CLIConfig) (bool, error) {
	if opts != nil && opts.clipboard && (opts.noClipboard || opts.noCopy) {
		return false, fmt.Errorf("--clipboard cannot be used with --no-clipboard")
	}
	clipboardEnabled := cfg.Clipboard
	if opts == nil {
		return clipboardEnabled, nil
	}
	if opts.clipboard {
		clipboardEnabled = true
	}
	if opts.noClipboard || opts.noCopy {
		clipboardEnabled = false
	}
	return clipboardEnabled, nil
}

func copyURLToClipboardIfRequested(cmd *cobra.Command, enabled bool, link string) {
	if !enabled {
		return
	}
	if err := clipboard.CopyText(link); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to copy URL to clipboard: %v\n", err)
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "copied URL to clipboard")
}

func inputData(args []string, clipboardEnabled bool) ([]byte, string, string, error) {
	if len(args) == 0 {
		if !clipboardEnabled {
			return nil, "", "", fmt.Errorf("no path provided; pass a file path or --clipboard to upload the current clipboard image")
		}
		data, filename, err := clipboard.ReadImagePNG()
		if err != nil {
			return nil, "", "", fmt.Errorf("no path provided and failed to read clipboard image: %w", err)
		}
		return data, filename, "image/png", nil
	}

	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", err
	}
	filename := filepath.Base(path)
	contentType := http.DetectContentType(data)
	if byExt := mimeTypeByExt(filename); byExt != "" {
		contentType = byExt
	}
	return data, filename, contentType, nil
}

func mimeTypeByExt(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log", ".md":
		return "text/plain"
	default:
		return ""
	}
}

func newListCommand() *cobra.Command {
	var endpoint string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List drops in this machine chain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			resp, err := ListDrops(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
			}
			for _, d := range resp.Drops {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d\t%s\t%s\n", d.ID, d.ExpiresAt.Format(time.RFC3339), d.Size, d.Filename, d.URL)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON response")
	return cmd
}

const (
	defaultPullDir      = "/tmp"
	defaultWatchTimeout = 5 * time.Minute
)

var pullWatchPollInterval = time.Second

func newPullCommand() *cobra.Command {
	var endpoint string
	var output string
	var force bool
	var watch bool
	var watchTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "pull [id]...",
		Short: "Download drops from this machine chain",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}
			if watch {
				if len(args) > 0 {
					return fmt.Errorf("--watch cannot be used with explicit drop IDs")
				}
				id, err := waitForNewImageDrop(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken, watchTimeout)
				if err != nil {
					return err
				}
				args = []string{id}
			} else if len(args) == 0 {
				resp, err := ListDrops(cmd.Context(), cfg.Endpoint, cfg.ChainSessionToken)
				if err != nil {
					return err
				}
				if len(resp.Drops) == 0 {
					return fmt.Errorf("no drops found")
				}
				args = []string{resp.Drops[0].ID}
			}

			return pullDrops(cmd, cfg.Endpoint, cfg.ChainSessionToken, args, output, force)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "service endpoint")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file or directory")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite output file")
	cmd.Flags().BoolVar(&watch, "watch", false, "wait for the next image uploaded after the command starts")
	cmd.Flags().DurationVar(&watchTimeout, "timeout", defaultWatchTimeout, "maximum time to wait with --watch; 0 disables the timeout")
	return cmd
}

func pullDrops(cmd *cobra.Command, endpoint, chainSessionToken string, ids []string, output string, force bool) error {
	outputIsDir := false
	if output != "" {
		if info, err := os.Stat(output); err == nil && info.IsDir() {
			outputIsDir = true
		} else if len(ids) > 1 {
			return fmt.Errorf("--output must be an existing directory when pulling multiple drops")
		}
	}

	for _, id := range ids {
		resp, err := PullDrop(cmd.Context(), endpoint, chainSessionToken, id)
		if err != nil {
			return err
		}
		path := outputPath(output, outputIsDir, id, resp.Filename)
		if output == "" {
			if err := os.MkdirAll(defaultPullDir, 0o700); err != nil {
				return err
			}
		}
		if !force {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists; pass --force to overwrite", path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.WriteFile(path, resp.Data, 0o600); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), path)
	}
	return nil
}

func waitForNewImageDrop(ctx context.Context, endpoint, chainSessionToken string, timeout time.Duration) (string, error) {
	if timeout < 0 {
		return "", fmt.Errorf("--timeout must be non-negative")
	}
	watchCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		watchCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	resp, err := ListDrops(watchCtx, endpoint, chainSessionToken)
	if err != nil {
		if errors.Is(watchCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("timed out waiting for a new image")
		}
		return "", err
	}
	seen := make(map[string]bool, len(resp.Drops))
	for _, d := range resp.Drops {
		seen[d.ID] = true
	}

	for {
		resp, err := ListDrops(watchCtx, endpoint, chainSessionToken)
		if err != nil {
			if errors.Is(watchCtx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("timed out waiting for a new image")
			}
			return "", err
		}
		if id, ok := firstNewImageDrop(resp.Drops, seen); ok {
			return id, nil
		}
		for _, d := range resp.Drops {
			seen[d.ID] = true
		}

		select {
		case <-watchCtx.Done():
			if errors.Is(watchCtx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("timed out waiting for a new image")
			}
			return "", watchCtx.Err()
		case <-time.After(pullWatchPollInterval):
		}
	}
}

func firstNewImageDrop(drops []DropSummary, seen map[string]bool) (string, bool) {
	for _, d := range drops {
		if seen[d.ID] || !isImageContentType(d.ContentType) {
			continue
		}
		return d.ID, true
	}
	return "", false
}

func isImageContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), "image/")
}

func outputPath(output string, outputIsDir bool, id, filename string) string {
	if filename == "" {
		filename = id
	}
	if output == "" {
		return filepath.Join(defaultPullDir, filename)
	}
	if outputIsDir {
		return filepath.Join(output, filename)
	}
	return output
}

func newVersionCommand(build BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "context-drop %s (%s, %s)\n", build.Version, build.Commit, build.Date)
			return err
		},
	}
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Manage local config"}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.CLIConfigPath()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Print config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			cfg.ChainSessionToken = redact(cfg.ChainSessionToken)
			return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set endpoint, default_ttl, clipboard, or machine_name",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			switch args[0] {
			case "endpoint":
				cfg.Endpoint = args[1]
			case "default_ttl", "ttl":
				d, err := time.ParseDuration(args[1])
				if err != nil {
					return err
				}
				cfg.DefaultTTL = d
			case "clipboard", "copy":
				b, err := strconv.ParseBool(args[1])
				if err != nil {
					return fmt.Errorf("parse clipboard: %w", err)
				}
				cfg.Clipboard = b
			case "machine_name":
				cfg.MachineName = args[1]
			default:
				return fmt.Errorf("unknown config key %q", args[0])
			}
			return config.SaveCLIConfig(cfg)
		},
	})
	return cmd
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local config and endpoint health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			if cfg.ChainSessionToken == "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "chain: missing")
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(), "chain: configured")
			}
			resp, err := http.Get(strings.TrimRight(cfg.Endpoint, "/") + "/health")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("endpoint health returned %s", resp.Status)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
}

func redact(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 10 {
		return "<redacted>"
	}
	return token[:6] + "..." + token[len(token)-4:]
}
