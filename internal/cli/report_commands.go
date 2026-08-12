package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const maxReportBytes = 4000

func newReportCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "report [message]",
		Short: "Report a natural-language update to the owning orchestrator",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message := ""
			if len(args) == 1 {
				message = args[0]
			} else {
				data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxReportBytes+1))
				if err != nil {
					return err
				}
				if len(data) > maxReportBytes {
					return fmt.Errorf("report message must be at most %d bytes", maxReportBytes)
				}
				message = string(data)
			}
			message = strings.TrimSpace(message)
			if message == "" {
				return fmt.Errorf("report message is required as an argument or on stdin")
			}
			if len([]byte(message)) > maxReportBytes {
				return fmt.Errorf("report message must be at most %d bytes", maxReportBytes)
			}
			endpoint, capability, runID := os.Getenv("CONTEXT_DROP_REPORT_URL"), os.Getenv("CONTEXT_DROP_REPORT_CAPABILITY"), os.Getenv("CONTEXT_DROP_RUN_ID")
			if endpoint == "" || capability == "" || runID == "" {
				return fmt.Errorf("worker reporting is not configured")
			}
			payload, err := json.Marshal(map[string]string{"runId": runID, "message": message})
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+capability)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("send report: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				return fmt.Errorf("report failed (%s): %s", resp.Status, strings.TrimSpace(string(data)))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "reported")
			return nil
		},
	}
}
