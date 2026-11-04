package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"contextdrop.dev/context-drop/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCommand(build BuildInfo) *cobra.Command {
	var check bool
	var version string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update context-drop to the latest release (or a chosen one) in place",
		Long: "Download the latest published release, check its checksum, and replace this binary with it.\n" +
			"--check only reports whether an update is available. --version installs a specific release,\n" +
			"including an older one. Binaries built from source report version dev and need --version.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := &http.Client{Timeout: 5 * time.Minute}
			target, err := os.Executable()
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(target); err == nil {
				target = resolved
			}
			want := ""
			if version != "" {
				if want, err = update.Normalize(version); err != nil {
					return err
				}
			} else {
				if want, err = update.Latest(cmd.Context(), client); err != nil {
					return err
				}
			}
			current := build.Version
			if check {
				fmt.Fprintf(cmd.OutOrStdout(), "current %s, available %s\n", current, want)
				return nil
			}
			if version == "" && current == "dev" {
				return fmt.Errorf("this binary was built from source (version dev); run `context-drop update --version %s` to install a release over it", want)
			}
			if current == want {
				fmt.Fprintf(cmd.OutOrStdout(), "context-drop %s is already installed at %s\n", want, target)
				return nil
			}
			if err := update.Install(cmd.Context(), client, want, target); err != nil {
				return fmt.Errorf("update failed, %s was not changed: %w", target, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated context-drop %s -> %s at %s\n", current, want, target)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report the current and available versions")
	cmd.Flags().StringVar(&version, "version", "", "install this release instead of the latest, e.g. v0.3.0")
	return cmd
}
