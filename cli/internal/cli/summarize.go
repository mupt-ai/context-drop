package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/summarize"
	"github.com/spf13/cobra"
)

func newSummarizeCommand() *cobra.Command {
	var flags config.SummarizeConfig
	var asJSON, force, noOverview bool
	var agentFilter string
	cmd := &cobra.Command{
		Use:   "summarize",
		Short: "Summarize what every live agent session is doing",
		Long: "Read the transcript of every live session and summarize it with the model you choose. Sessions whose\n" +
			"transcripts have not changed since the last run reuse their previous summary, so repeated runs only\n" +
			"pay for what changed. State lives in ~/.context-drop/summaries.json.\n\n" +
			"Providers (name one with --provider or summarize_provider; nothing is inferred from the environment):\n" +
			"anthropic (--model and --api-key-env), openai or any OpenAI-compatible server (--model, --base-url, e.g.\n" +
			"LM Studio, llama.cpp, vLLM), ollama (--model, default http://localhost:11434), or command (--command, any\n" +
			"CLI that reads the prompt on stdin, e.g. 'claude -p').\n" +
			"Settings come from summarize_* keys in ~/.context-drop/config.toml or CONTEXT_DROP_SUMMARIZE_* variables.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadCLIConfig()
			if err != nil {
				return err
			}
			sc := mergeSummarize(cfg.Summarize, flags)
			model, err := summarize.NewModel(sc, os.Getenv, nil)
			if err != nil {
				return err
			}
			env, providers, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			printWarnings(cmd.ErrOrStderr(), idx)
			inputs := summarizeInputs(cmd, env, providers, idx, agentFilter)
			s := &summarize.Summarizer{
				Model:     model,
				StatePath: summarize.StatePath(env.ContextDropHome()),
				Force:     force,
				Overview:  !noOverview && len(inputs) > 1,
			}
			report, err := s.Run(cmd.Context(), inputs)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			printReport(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.Provider, "provider", "", "anthropic, openai, ollama, or command")
	cmd.Flags().StringVar(&flags.Model, "model", "", "model name")
	cmd.Flags().StringVar(&flags.BaseURL, "base-url", "", "API base URL (OpenAI-compatible or Anthropic)")
	cmd.Flags().StringVar(&flags.APIKeyEnv, "api-key-env", "", "environment variable that holds the API key")
	cmd.Flags().StringVar(&flags.Command, "command", "", "shell command that reads the prompt on stdin and prints the summary")
	cmd.Flags().StringVar(&agentFilter, "agent", "", "only summarize one agent kind")
	cmd.Flags().BoolVar(&force, "force", false, "re-summarize sessions even if nothing changed")
	cmd.Flags().BoolVar(&noOverview, "no-overview", false, "skip the cross-session overview")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func mergeSummarize(base, override config.SummarizeConfig) config.SummarizeConfig {
	if override.Provider != "" && override.Provider != base.Provider {
		// A different provider should not inherit another provider's model or endpoint.
		base = config.SummarizeConfig{Provider: override.Provider}
	}
	for _, pair := range []struct {
		dst *string
		v   string
	}{
		{&base.Model, override.Model}, {&base.BaseURL, override.BaseURL},
		{&base.APIKeyEnv, override.APIKeyEnv}, {&base.Command, override.Command},
	} {
		if pair.v != "" {
			*pair.dst = pair.v
		}
	}
	return base
}

func summarizeInputs(cmd *cobra.Command, env *agents.Env, providers []agents.Provider, idx agents.Index, agentFilter string) []summarize.Input {
	subs := map[string][]agents.Session{}
	for _, s := range idx.Sessions {
		if s.Subagent {
			subs[s.ParentID] = append(subs[s.ParentID], s)
		}
	}
	var inputs []summarize.Input
	for _, s := range idx.Sessions {
		if agentFilter != "" && s.Agent != agentFilter {
			continue
		}
		// Summarize subagents only when their parent is not live; otherwise they are part of the parent's story.
		if s.Subagent {
			if _, ok := idx.Parent(s); ok {
				continue
			}
		}
		in := summarize.Input{Session: s, Subagents: subs[s.ID]}
		if p, err := agents.ProviderFor(providers, s.Agent); err == nil {
			in.Messages, in.Err = p.Transcript(cmd.Context(), env, s)
		}
		inputs = append(inputs, in)
	}
	return inputs
}

func printReport(w io.Writer, r summarize.Report) {
	if len(r.Sessions) == 0 {
		fmt.Fprintln(w, "No live agent sessions.")
		return
	}
	if r.Overview != "" {
		fmt.Fprintf(w, "What's going on\n%s\n\n", r.Overview)
	}
	changed := 0
	for _, s := range r.Sessions {
		if s.Changed {
			changed++
		}
		head := fmt.Sprintf("%s  %s  %s  %s", s.ID, s.Agent, s.Status, shortPath(s.Cwd))
		if s.Name != "" {
			head += "  " + truncateRunes(s.Name, 60)
		}
		if s.Subagents > 0 {
			head += fmt.Sprintf("  (+%d subagents)", s.Subagents)
		}
		fmt.Fprintln(w, head)
		note := "updated"
		if !s.Changed {
			note = "no changes since " + s.SummarizedAt.Local().Format("Jan 2 15:04")
		}
		if s.Error != "" {
			note = "error: " + s.Error
		}
		fmt.Fprintf(w, "  [%s]\n", note)
		if s.Summary != "" {
			for _, line := range strings.Split(s.Summary, "\n") {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%d of %d sessions changed · %s · %s\n", changed, len(r.Sessions), r.Model, r.GeneratedAt.Local().Format(time.Kitchen))
}
