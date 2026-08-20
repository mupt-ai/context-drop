package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	cmd := &cobra.Command{
		Use:               "context-drop",
		Short:             "Run the local Context Drop orchestration daemon and utilities",
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	cmd.AddCommand(newUploadCommand())
	cmd.AddCommand(newReportCommand())
	cmd.AddCommand(newScheduleCommand())
	cmd.AddCommand(newRepoCommand())
	cmd.AddCommand(newDaemonCommand())
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

func newUploadCommand() *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "upload [path]",
		Short: "Upload a file or, with --clipboard, the current clipboard image",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpload(cmd.Context(), cmd, opts, args)
		},
	}
	addUploadFlags(cmd, opts)
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
	if cfg.UploadToken == "" {
		return fmt.Errorf("upload token is required; set CONTEXT_DROP_UPLOAD_TOKEN or upload_token in config")
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
		Endpoint: cfg.Endpoint, UploadToken: cfg.UploadToken, Filename: drop.SafeFilename(filename),
		ContentType: contentType, TTL: cfg.DefaultTTL, Data: data,
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

func effectiveClipboard(opts *options, cfg config.CLIConfig) (bool, error) {
	if opts != nil && opts.clipboard && (opts.noClipboard || opts.noCopy) {
		return false, fmt.Errorf("--clipboard cannot be used with --no-clipboard")
	}
	enabled := cfg.Clipboard
	if opts == nil {
		return enabled, nil
	}
	if opts.clipboard {
		enabled = true
	}
	if opts.noClipboard || opts.noCopy {
		enabled = false
	}
	return enabled, nil
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

func newVersionCommand(build BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print version information", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "context-drop %s (%s, %s)\n", build.Version, build.Commit, build.Date)
			return err
		},
	}
}
