package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newIMessageCommand() *cobra.Command {
	root := &cobra.Command{Use: "imessage", Short: "Configure the private local iMessage/SMS request adapter"}
	root.AddCommand(newIMessageSetupCommand(), newIMessageStatusCommand(), newIMessageLatencyCommand())
	return root
}

func newIMessageSetupCommand() *cobra.Command {
	var chatID, recipient, imsgPath, agent, personaFile, memoryFile, conversationArchiveFile, sessionFile, responderCwd string
	var poll, historyTimeout, responderTimeout, sendTimeout time.Duration
	var syncLimit, maxMessageBytes, maxReplyBytes int
	var responderArgs []string
	var disabled, trusted, useMigratedModel bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Save local iMessage adapter settings without sending a message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if chatID == "" {
				return fmt.Errorf("--chat-id is required; discover it with: imsg chats --json")
			}
			if poll < 100*time.Millisecond || poll%time.Millisecond != 0 {
				return fmt.Errorf("--poll must be a whole number of milliseconds and at least 100ms")
			}
			resolvedImsg, err := resolveIMessageExecutable(imsgPath)
			if err != nil {
				return err
			}
			if memoryFile == "" {
				path, memoryErr := imessage.DefaultMemoryFile()
				if memoryErr != nil {
					return memoryErr
				}
				memoryFile = path
			}
			if conversationArchiveFile == "" {
				path, archiveErr := imessage.DefaultConversationArchiveFile()
				if archiveErr != nil {
					return archiveErr
				}
				conversationArchiveFile = path
			}
			if sessionFile == "" {
				path, sessionErr := imessage.DefaultSessionFile()
				if sessionErr != nil {
					return sessionErr
				}
				sessionFile = path
			}
			responder, err := responderCommand(agent, responderArgs, trusted, useMigratedModel, sessionFile)
			if err != nil {
				return err
			}
			if personaFile == "" {
				home, homeErr := imessage.DefaultPersonaFile()
				if homeErr == nil && home != "" {
					personaFile = home
				}
			}
			cfg := imessage.Defaults()
			cfg.Enabled = !disabled
			cfg.Trusted = trusted
			cfg.ChatID = chatID
			cfg.Recipient = recipient
			cfg.ImsgPath = resolvedImsg
			cfg.PollMilliseconds = int(poll / time.Millisecond)
			cfg.PollSeconds = 0
			cfg.SyncLimit = syncLimit
			cfg.HistoryTimeoutSeconds = durationSeconds(historyTimeout)
			cfg.ResponderTimeoutSeconds = durationSeconds(responderTimeout)
			cfg.SendTimeoutSeconds = durationSeconds(sendTimeout)
			cfg.MaxMessageBytes = maxMessageBytes
			cfg.MaxReplyBytes = maxReplyBytes
			if personaFile != "" {
				cfg.PersonaFile = personaFile
			}
			if memoryFile != "" {
				cfg.MemoryFile = memoryFile
			}
			if conversationArchiveFile != "" {
				cfg.ConversationArchiveFile = conversationArchiveFile
			}
			if trusted && responderCwd == "" {
				responderCwd, err = imessage.DefaultResponderCwd()
				if err != nil {
					return fmt.Errorf("create private orchestrator directory: %w", err)
				}
			}
			cfg.ResponderCwd = responderCwd
			cfg.ResponderCommand = responder
			if err := imessage.Save(cfg); err != nil {
				return err
			}
			_, path, _ := imessage.Paths()
			fmt.Fprintf(cmd.OutOrStdout(), "saved private iMessage config at %s\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "No message was sent. Restart the Context Drop daemon to apply it: context-drop daemon restart")
			return nil
		},
	}
	cmd.Flags().StringVar(&chatID, "chat-id", "", "Messages chat ID from `imsg chats --json`")
	cmd.Flags().StringVar(&recipient, "recipient", "", "optional expected phone/email label (informational)")
	cmd.Flags().StringVar(&imsgPath, "imsg-path", "", "absolute imsg executable path (default: detected on PATH)")
	cmd.Flags().StringVar(&agent, "agent", "pi", "local responder preset (currently pi)")
	cmd.Flags().DurationVar(&poll, "poll", time.Duration(imessage.DefaultPollMilliseconds)*time.Millisecond, "watch debounce and legacy polling interval")
	cmd.Flags().IntVar(&syncLimit, "sync-limit", imessage.DefaultSyncLimit, "recent history items per poll (1..200)")
	cmd.Flags().DurationVar(&historyTimeout, "history-timeout", imessage.DefaultHistoryTimeoutSeconds*time.Second, "imsg history timeout")
	cmd.Flags().DurationVar(&responderTimeout, "responder-timeout", imessage.DefaultResponderTimeoutSeconds*time.Second, "local responder timeout")
	cmd.Flags().DurationVar(&sendTimeout, "send-timeout", imessage.DefaultSendTimeoutSeconds*time.Second, "imsg send timeout")
	cmd.Flags().IntVar(&maxMessageBytes, "max-message-bytes", imessage.DefaultMaxMessageBytes, "maximum inbound text bytes")
	cmd.Flags().IntVar(&maxReplyBytes, "max-reply-bytes", imessage.DefaultMaxReplyBytes, "maximum responder output bytes")
	cmd.Flags().StringVar(&personaFile, "persona-file", "", "absolute path to a private persona/voice file prepended to each responder prompt (default: ~/.context-drop/SOUL.md if present)")
	cmd.Flags().StringVar(&memoryFile, "memory-file", "", "absolute durable conversation-memory file prepended to each responder prompt (default: ~/.context-drop/MEMORY.md if present)")
	cmd.Flags().StringVar(&conversationArchiveFile, "conversation-archive-file", "", "absolute JSONL corpus of full chat history used for verbatim beginning + retrieval excerpts in each responder prompt (default: ~/.context-drop/sessions/chat_full.jsonl if present)")
	cmd.Flags().StringVar(&sessionFile, "session-file", "", "absolute Pi session file to continue (default: ~/.context-drop/sessions/imessage.jsonl if present)")
	cmd.Flags().StringVar(&responderCwd, "responder-cwd", "", "absolute working directory for the responder (trusted default: ~/.context-drop/orchestrator)")
	cmd.Flags().StringArrayVar(&responderArgs, "responder-arg", nil, "explicit responder argv element; repeat, and include {prompt_file}")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "save configuration without enabling polling")
	cmd.Flags().BoolVar(&trusted, "trusted", false, "enable persistent tool-enabled Pi orchestration for this explicitly configured private chat")
	cmd.Flags().BoolVar(&useMigratedModel, "use-migrated-model", false, "use the migrated routing model (dari-prod/dari/routing) for the pi responder")
	return cmd
}

func durationSeconds(value time.Duration) int {
	if value <= 0 || value%time.Second != 0 {
		return 0
	}
	return int(value / time.Second)
}

func resolveIMessageExecutable(value string) (string, error) {
	if value == "" {
		path, err := runtimeclient.ResolveExecutable("imsg")
		if err != nil {
			return "", fmt.Errorf("find imsg: %w", err)
		}
		return path, nil
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("--imsg-path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err == nil {
		value = resolved
	}
	info, err := os.Stat(value)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("--imsg-path must point to an executable file")
	}
	return value, nil
}

func responderCommand(agent string, explicit []string, trusted, useMigratedModel bool, sessionFile string) ([]string, error) {
	if len(explicit) > 0 {
		return explicit, nil
	}
	if agent != "pi" {
		return nil, fmt.Errorf("unknown safe responder preset %q; use pi or explicit repeated --responder-arg values", agent)
	}
	cfg, err := runtimeclient.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("load local agents: %w", err)
	}
	pi, ok := cfg.Agents["pi"]
	if !ok || len(pi.Command) == 0 || !filepath.IsAbs(pi.Command[0]) {
		return nil, fmt.Errorf("Pi is not configured locally; run context-drop init or provide explicit --responder-arg values")
	}
	argv := []string{pi.Command[0], "--print"}
	if !trusted {
		argv = append(argv, "--no-context-files", "--no-tools", "--no-extensions")
	}
	if sessionFile == "" {
		argv = append(argv, "--no-session")
	} else {
		if !filepath.IsAbs(sessionFile) {
			return nil, fmt.Errorf("--session-file must be absolute")
		}
		info, statErr := os.Stat(sessionFile)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("--session-file must point to a regular file")
		}
		argv = append(argv, "--session", sessionFile)
	}
	if useMigratedModel {
		argv = append(argv, "--model", imessage.MigratedPiModel)
	}
	argv = append(argv, "@{prompt_file}")
	return argv, nil
}

func newIMessageStatusCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show private iMessage configuration and daemon processing state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := imessage.Load()
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("iMessage is not configured; run context-drop imessage setup --chat-id ID")
			}
			if err != nil {
				return err
			}
			store, err := orchestrator.NewStore()
			if err != nil {
				return err
			}
			state, err := store.Load()
			if err != nil {
				return err
			}
			out := struct {
				Enabled        bool                               `json:"enabled"`
				Trusted        bool                               `json:"trusted"`
				ChatID         string                             `json:"chat_id"`
				Recipient      string                             `json:"recipient,omitempty"`
				ImsgPath       string                             `json:"imsg_path"`
				PollInterval   string                             `json:"poll_interval"`
				Initialized    bool                               `json:"initialized"`
				LastPollAt     *time.Time                         `json:"last_poll_at,omitempty"`
				LastError      string                             `json:"last_error,omitempty"`
				ProcessedCount int                                `json:"processed_count"`
				Jobs           map[string]orchestrator.MessageJob `json:"jobs,omitempty"`
			}{cfg.Enabled, cfg.Trusted, cfg.ChatID, cfg.Recipient, cfg.ImsgPath, cfg.PollInterval().String(), state.IMessageInitialized, state.LastMessagePollAt, state.LastMessageError, len(state.SeenMessageIDs), state.MessageJobs}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Enabled: %t\nTrusted orchestration: %t\nChat: %s\nRecipient: %s\nimsg: %s\nPoll: %s\nInitialized: %t\nProcessed IDs: %d\n", out.Enabled, out.Trusted, out.ChatID, out.Recipient, out.ImsgPath, out.PollInterval, out.Initialized, out.ProcessedCount)
			if out.LastPollAt != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Last poll: %s\n", out.LastPollAt.Format(time.RFC3339))
			}
			if out.LastError != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Last error: %s\n", out.LastError)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

type latencyDistribution struct {
	Count  int     `json:"count"`
	MinMS  int64   `json:"min_ms"`
	P50MS  int64   `json:"p50_ms"`
	P90MS  int64   `json:"p90_ms"`
	P95MS  int64   `json:"p95_ms"`
	MaxMS  int64   `json:"max_ms"`
	MeanMS float64 `json:"mean_ms"`
}

type iMessageLatencyReport struct {
	Definition string                         `json:"definition"`
	SampleSize int                            `json:"sample_size"`
	Minimum    int                            `json:"minimum_sample_size"`
	Metrics    map[string]latencyDistribution `json:"metrics"`
	Models     map[string]int                 `json:"selected_models,omitempty"`
	Target     struct {
		P50MS int64 `json:"p50_ms"`
		P90MS int64 `json:"p90_ms"`
		Met   bool  `json:"met"`
	} `json:"target"`
}

func newIMessageLatencyCommand() *cobra.Command {
	var jsonOut bool
	var last, minimum int
	cmd := &cobra.Command{
		Use:   "latency",
		Short: "Report reproducible iMessage processing latency distributions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := orchestrator.NewStore()
			if err != nil {
				return err
			}
			state, err := store.Load()
			if err != nil {
				return err
			}
			report, err := buildIMessageLatencyReport(state.MessageJobs, last, minimum)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			endToEnd := report.Metrics["end_to_end"]
			fmt.Fprintf(cmd.OutOrStdout(), "Sample: %d instrumented sent messages (latest %d requested; minimum %d)\n", report.SampleSize, last, minimum)
			fmt.Fprintf(cmd.OutOrStdout(), "End-to-end: p50=%s p90=%s p95=%s min=%s max=%s mean=%s\n", durationMillis(endToEnd.P50MS), durationMillis(endToEnd.P90MS), durationMillis(endToEnd.P95MS), durationMillis(endToEnd.MinMS), durationMillis(endToEnd.MaxMS), durationMillis(int64(endToEnd.MeanMS)))
			fmt.Fprintf(cmd.OutOrStdout(), "Target p50<=3s and p90<=8s: %t\n", report.Target.Met)
			return nil
		},
	}
	cmd.Flags().IntVar(&last, "last", 50, "number of latest instrumented sent messages to include")
	cmd.Flags().IntVar(&minimum, "minimum-sample", 20, "minimum sample size required to report the target as met")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func buildIMessageLatencyReport(jobs map[string]orchestrator.MessageJob, last, minimum int) (iMessageLatencyReport, error) {
	if last < 1 || minimum < 1 {
		return iMessageLatencyReport{}, fmt.Errorf("--last and --minimum-sample must be positive")
	}
	sample := make([]orchestrator.MessageJob, 0, len(jobs))
	for _, job := range jobs {
		if job.Status == "sent" && job.SentAt != nil && job.Latency.MessageCreatedAt != nil && job.Latency.EndToEndMS > 0 {
			sample = append(sample, job)
		}
	}
	sort.Slice(sample, func(i, j int) bool { return sample[i].SentAt.Before(*sample[j].SentAt) })
	if len(sample) > last {
		sample = sample[len(sample)-last:]
	}
	if len(sample) == 0 {
		return iMessageLatencyReport{}, fmt.Errorf("no instrumented sent iMessage jobs are available yet")
	}
	values := func(read func(orchestrator.MessageLatency) int64) []int64 {
		out := make([]int64, 0, len(sample))
		for _, job := range sample {
			out = append(out, read(job.Latency))
		}
		return out
	}
	report := iMessageLatencyReport{
		Definition: "latest successfully sent iMessages with parseable source timestamps; end_to_end is source creation through completed imsg send; nearest-rank percentiles",
		SampleSize: len(sample),
		Minimum:    minimum,
		Metrics: map[string]latencyDistribution{
			"end_to_end":        distribution(values(func(v orchestrator.MessageLatency) int64 { return v.EndToEndMS })),
			"queue":             distribution(values(func(v orchestrator.MessageLatency) int64 { return v.QueueMS })),
			"worker_queue":      distribution(values(func(v orchestrator.MessageLatency) int64 { return v.WorkerQueueMS })),
			"history":           distribution(values(func(v orchestrator.MessageLatency) int64 { return v.HistoryMS })),
			"prompt_build":      distribution(values(func(v orchestrator.MessageLatency) int64 { return v.PromptBuildMS })),
			"responder_startup": distribution(values(func(v orchestrator.MessageLatency) int64 { return v.ResponderStartupMS })),
			"responder":         distribution(values(func(v orchestrator.MessageLatency) int64 { return v.ResponderMS })),
			"first_output":      distribution(values(func(v orchestrator.MessageLatency) int64 { return v.FirstOutputMS })),
			"tool_execution":    distribution(values(func(v orchestrator.MessageLatency) int64 { return v.ToolExecutionMS })),
			"compaction":        distribution(values(func(v orchestrator.MessageLatency) int64 { return v.CompactionMS })),
			"send":              distribution(values(func(v orchestrator.MessageLatency) int64 { return v.SendMS })),
			"service":           distribution(values(func(v orchestrator.MessageLatency) int64 { return v.ServiceMS })),
		},
		Models: map[string]int{},
	}
	var roundDurations []int64
	for _, job := range sample {
		for _, round := range job.Latency.ModelRounds {
			roundDurations = append(roundDurations, round.DurationMS)
			if round.Model != "" {
				report.Models[round.Model]++
			}
		}
	}
	if len(roundDurations) > 0 {
		report.Metrics["model_round"] = distribution(roundDurations)
	}
	report.Target.P50MS = 3000
	report.Target.P90MS = 8000
	endToEnd := report.Metrics["end_to_end"]
	report.Target.Met = len(sample) >= minimum && endToEnd.P50MS <= report.Target.P50MS && endToEnd.P90MS <= report.Target.P90MS
	return report, nil
}

func distribution(values []int64) latencyDistribution {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var sum int64
	for _, value := range values {
		sum += value
	}
	nearestRank := func(percent int) int64 {
		index := (percent*len(values) + 99) / 100
		if index < 1 {
			index = 1
		}
		return values[index-1]
	}
	return latencyDistribution{Count: len(values), MinMS: values[0], P50MS: nearestRank(50), P90MS: nearestRank(90), P95MS: nearestRank(95), MaxMS: values[len(values)-1], MeanMS: float64(sum) / float64(len(values))}
}

func durationMillis(value int64) time.Duration {
	return time.Duration(value) * time.Millisecond
}
