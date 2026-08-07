package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newIMessageCommand() *cobra.Command {
	root := &cobra.Command{Use: "imessage", Short: "Configure the private local iMessage/SMS request adapter"}
	root.AddCommand(newIMessageSetupCommand(), newIMessageStatusCommand())
	return root
}

func newIMessageSetupCommand() *cobra.Command {
	var chatID, recipient, imsgPath, agent, personaFile, memoryFile, conversationArchiveFile, sessionFile string
	var poll, historyTimeout, responderTimeout, sendTimeout time.Duration
	var syncLimit, maxMessageBytes, maxReplyBytes int
	var responderArgs []string
	var disabled, useMigratedModel bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Save local iMessage adapter settings without sending a message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if chatID == "" {
				return fmt.Errorf("--chat-id is required; discover it with: imsg chats --json")
			}
			if poll < time.Second || poll%time.Second != 0 {
				return fmt.Errorf("--poll must be a whole number of seconds and at least 1s")
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
			responder, err := responderCommand(agent, responderArgs, useMigratedModel, sessionFile)
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
			cfg.ChatID = chatID
			cfg.Recipient = recipient
			cfg.ImsgPath = resolvedImsg
			cfg.PollSeconds = int(poll / time.Second)
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
	cmd.Flags().DurationVar(&poll, "poll", 3*time.Second, "history polling interval")
	cmd.Flags().IntVar(&syncLimit, "sync-limit", imessage.DefaultSyncLimit, "recent history items per poll (1..200)")
	cmd.Flags().DurationVar(&historyTimeout, "history-timeout", imessage.DefaultHistoryTimeoutSeconds*time.Second, "imsg history timeout")
	cmd.Flags().DurationVar(&responderTimeout, "responder-timeout", imessage.DefaultResponderTimeoutSeconds*time.Second, "local responder timeout")
	cmd.Flags().DurationVar(&sendTimeout, "send-timeout", imessage.DefaultSendTimeoutSeconds*time.Second, "imsg send timeout")
	cmd.Flags().IntVar(&maxMessageBytes, "max-message-bytes", imessage.DefaultMaxMessageBytes, "maximum inbound text bytes")
	cmd.Flags().IntVar(&maxReplyBytes, "max-reply-bytes", imessage.DefaultMaxReplyBytes, "maximum responder output bytes")
	cmd.Flags().StringVar(&personaFile, "persona-file", "", "absolute path to a private persona/context file prepended to each responder prompt (default: ~/.context-drop/SOUL.md if present)")
	cmd.Flags().StringVar(&memoryFile, "memory-file", "", "absolute durable conversation-memory file prepended to each responder prompt (default: ~/.context-drop/MEMORY.md if present)")
	cmd.Flags().StringVar(&conversationArchiveFile, "conversation-archive-file", "", "absolute JSONL corpus of full chat history used for verbatim beginning + retrieval excerpts in each responder prompt (default: ~/.context-drop/sessions/chat_full.jsonl if present)")
	cmd.Flags().StringVar(&sessionFile, "session-file", "", "absolute Pi session file to continue (default: ~/.context-drop/sessions/imessage.jsonl if present)")
	cmd.Flags().StringArrayVar(&responderArgs, "responder-arg", nil, "explicit responder argv element; repeat, and include {prompt_file}")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "save configuration without enabling polling")
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

func responderCommand(agent string, explicit []string, useMigratedModel bool, sessionFile string) ([]string, error) {
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
	argv := []string{pi.Command[0], "--print", "--no-context-files", "--no-tools", "--no-extensions"}
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
				ChatID         string                             `json:"chat_id"`
				Recipient      string                             `json:"recipient,omitempty"`
				ImsgPath       string                             `json:"imsg_path"`
				PollSeconds    int                                `json:"poll_seconds"`
				Initialized    bool                               `json:"initialized"`
				LastPollAt     *time.Time                         `json:"last_poll_at,omitempty"`
				LastError      string                             `json:"last_error,omitempty"`
				ProcessedCount int                                `json:"processed_count"`
				Jobs           map[string]orchestrator.MessageJob `json:"jobs,omitempty"`
			}{cfg.Enabled, cfg.ChatID, cfg.Recipient, cfg.ImsgPath, cfg.PollSeconds, state.IMessageInitialized, state.LastMessagePollAt, state.LastMessageError, len(state.SeenMessageIDs), state.MessageJobs}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Enabled: %t\nChat: %s\nRecipient: %s\nimsg: %s\nPoll: %ds\nInitialized: %t\nProcessed IDs: %d\n", out.Enabled, out.ChatID, out.Recipient, out.ImsgPath, out.PollSeconds, out.Initialized, out.ProcessedCount)
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
