package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"contextdrop.dev/context-drop/internal/pool"
	"github.com/spf13/cobra"
)

// Seams for tests.
var (
	newEnv       = agents.NewEnv
	allProviders = agents.Providers
)

func discover(ctx context.Context) (*agents.Env, []agents.Provider, agents.Index, error) {
	env, err := newEnv()
	if err != nil {
		return nil, nil, agents.Index{}, err
	}
	providers := allProviders()
	return env, providers, agents.Discover(ctx, env, providers), nil
}

func printWarnings(w io.Writer, idx agents.Index) {
	for _, warning := range idx.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func newListCommand() *cobra.Command {
	var asJSON, topLevelOnly bool
	var filter sessionFilter
	var org orgFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List live agent sessions and subagents with their IDs",
		Long: "List every live Claude Code, Codex, Pi, and OpenCode session on this machine, including subagents.\n" +
			"IDs are deterministic: the same session always gets the same ID.\n\n" +
			"Sessions are found from their processes, wherever they run. WHERE shows the tmux, herdr, or rex pane a\n" +
			"session runs in, or the desktop app (claude desktop, codex desktop) hosting it; --in, --session, --window,\n" +
			"and --pane keep only the sessions in that place. CLAIMS shows what each session has claimed with `context-drop claim`. A warning names worktrees that several sessions\n" +
			"share; `context-drop overlaps` also checks the files they edit.\n\n" +
			"Once this machine has joined an org (`context-drop join`), list shows every agent in the org, with IDs\n" +
			"like <machine>/<session>; --machine and --cwd narrow it, and --local shows only this machine as before.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !org.local {
				m, isJoined, err := joined()
				if err != nil {
					return err
				}
				if isJoined {
					sel, err := org.selector(filter)
					if err != nil {
						return err
					}
					return orgList(cmd, m, sel, topLevelOnly, asJSON)
				}
			}
			if org.machine != "" {
				return fmt.Errorf("--machine works across an org; this machine has not joined one, or --local was given")
			}
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			printWarnings(cmd.ErrOrStderr(), idx)
			agents.Locate(cmd.Context(), env, idx.Sessions)
			attachClaims(cmd, env, idx)
			sessions := filter.apply(cmd.Context(), env, idx.Sessions)
			if org.cwd != "" {
				dir, err := filepath.Abs(org.cwd)
				if err != nil {
					return err
				}
				kept := sessions[:0]
				for _, s := range sessions {
					if (pool.Selector{Cwd: dir}).Matches(pool.Agent{Cwd: s.Cwd}) {
						kept = append(kept, s)
					}
				}
				sessions = kept
			}
			if topLevelOnly {
				sessions = topLevel(sessions)
			}
			for _, o := range agents.WorktreeOverlaps(sessions) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s share %s; see `context-drop overlaps`\n", strings.Join(o.Sessions, ", "), shortPath(o.Path))
			}
			if asJSON {
				if sessions == nil {
					sessions = []agents.Session{}
				}
				return writeJSON(cmd.OutOrStdout(), sessions)
			}
			if len(sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No live agent sessions.")
				return nil
			}
			claimed := false
			for _, s := range sessions {
				claimed = claimed || len(s.Claims) > 0
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			header := "ID\tAGENT\tSTATUS\tACTIVE\tCWD\tNAME\tWHERE"
			if claimed {
				header += "\tCLAIMS"
			}
			fmt.Fprintln(w, header)
			for _, s := range sessions {
				id := s.ID
				if s.Subagent {
					id = "└ " + id
				}
				where := "-"
				if s.Location != nil {
					where = s.Location.String()
				} else if s.App != "" {
					where = agents.AppLabel(s.App)
				}
				row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s", id, s.Agent, s.Status, ago(s.UpdatedAt), shortPath(s.Cwd), label(s), where)
				if claimed {
					row += "\t" + truncateRunes(strings.Join(s.Claims, ", "), 60)
				}
				fmt.Fprintln(w, row)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&topLevelOnly, "top-level", false, "hide subagents")
	filter.register(cmd, "only show")
	org.register(cmd, "only show")
	return cmd
}

// attachClaims fills in each session's live claims. A claims file that
// cannot be read is reported and otherwise ignored.
func attachClaims(cmd *cobra.Command, env *agents.Env, idx agents.Index) {
	claims, err := env.Claims().Live(idx)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: claims: %v\n", err)
		return
	}
	for _, c := range claims {
		for i := range idx.Sessions {
			if idx.Sessions[i].ID == c.Session {
				idx.Sessions[i].Claims = append(idx.Sessions[i].Claims, c.Target)
			}
		}
	}
}

func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show TARGET",
		Short: "Print everything known about one session as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			s, err := idx.Resolve(args[0])
			if err != nil {
				return err
			}
			located := []agents.Session{s}
			agents.Locate(cmd.Context(), env, located)
			return writeJSON(cmd.OutOrStdout(), located[0])
		},
	}
}

func newReadCommand() *cobra.Command {
	var count int
	var asJSON, screen, allRuns bool
	var after string
	cmd := &cobra.Command{
		Use:   "read TARGET",
		Short: "Print the recent transcript of a session",
		Long: "Print the last messages of a session's transcript. --screen prints the last lines of the tmux, herdr,\n" +
			"or rex pane the session runs in instead (-n lines, default 40), which shows prompts and dialogs that\n" +
			"never reach the transcript.\n\n" +
			"An org agent ID (<machine>/<session>) reads that agent wherever it runs. It shows the current context\n" +
			"run, from the last compaction on; --all shows every run, and --after shows only entries newer than the\n" +
			"cursor a previous `read --json` returned.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, machine, session, isOrg, err := orgID(args[0])
			if err != nil {
				return err
			}
			switch {
			case isOrg && screen:
				if machine != m.Machine {
					return fmt.Errorf("--screen works only for agents on this machine")
				}
				args[0] = session
			case isOrg:
				res, err := orgRead(cmd.Context(), m, machine, session, pool.ReadOptions{Last: count, All: allRuns, After: after})
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), res)
				}
				printEntries(cmd.OutOrStdout(), res.Entries)
				return nil
			case allRuns || after != "":
				return fmt.Errorf("--all and --after work with org agent IDs (<machine>/<session>)")
			}
			env, providers, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			s, err := idx.Resolve(args[0])
			if err != nil {
				return err
			}
			if screen {
				if !cmd.Flags().Changed("messages") {
					count = 40
				}
				text, err := agents.Capture(cmd.Context(), env, s, count)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), text)
				return nil
			}
			p, err := agents.ProviderFor(providers, s.Agent)
			if err != nil {
				return err
			}
			msgs, err := p.Transcript(cmd.Context(), env, s)
			if err != nil {
				return err
			}
			msgs = lastMessages(msgs, count)
			if asJSON {
				if msgs == nil {
					msgs = []agents.Message{}
				}
				return writeJSON(cmd.OutOrStdout(), msgs)
			}
			printMessages(cmd.OutOrStdout(), msgs)
			return nil
		},
	}
	cmd.Flags().IntVarP(&count, "messages", "n", 20, "number of recent messages to print (0 for all), or lines with --screen")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&screen, "screen", false, "print the session's terminal pane instead of its transcript")
	cmd.Flags().BoolVar(&allRuns, "all", false, "with an org agent ID, every context run, not just the one since the last compaction")
	cmd.Flags().StringVar(&after, "after", "", "with an org agent ID, only entries newer than this cursor")
	cmd.MarkFlagsMutuallyExclusive("screen", "json")
	return cmd
}

func newSendCommand() *cobra.Command {
	var from string
	var files []string
	var wait, all bool
	var timeout time.Duration
	var filter sessionFilter
	var org orgFlags
	cmd := &cobra.Command{
		Use:   "send TARGET MESSAGE... | send --all [filters] MESSAGE...",
		Short: "Send a message to a live session or subagent, or to a group of sessions",
		Long: "Deliver MESSAGE into a live session as if it were the next prompt. Use - as MESSAGE to read it from stdin.\n\n" +
			"--all sends it to every live top-level session that matches --agent, --in, --session, --window, and --pane,\n" +
			"except the session running this command. With --wait, it waits for every reply and prints them in turn.\n\n" +
			"Delivery uses each agent's own channel: Claude Code's peer socket, `codex queue`, the OpenCode service API,\n" +
			"the Context Drop Pi extension (see `context-drop setup pi`), or for Pi sessions without it, the tmux, herdr,\n" +
			"or rex pane they run in. Claude Code subagents receive messages through their parent session.\n\n" +
			"--file hands the recipient a file on this machine: its absolute path is listed under the message, so the\n" +
			"agent reads it in place and nothing is uploaded. Use `context-drop upload` for a link to share elsewhere.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if filter.set() && !all {
				return fmt.Errorf("--agent, --in, --session, --window, and --pane need --all")
			}
			if !all && len(args) < 2 {
				return fmt.Errorf("give a TARGET and a MESSAGE, or --all and a MESSAGE")
			}
			words := args
			if !all {
				words = args[1:]
			}
			text := strings.Join(words, " ")
			if text == "-" {
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(data)
			}
			text = strings.TrimSpace(text)
			if !all || !org.local {
				// An org target, or --all across a joined org, goes through the server.
				var target pool.Selector
				var m pool.Membership
				if all {
					var isJoined bool
					var err error
					if m, isJoined, err = joined(); err != nil {
						return err
					}
					if isJoined {
						if target, err = org.selector(filter); err != nil {
							return err
						}
						target.All = true
					}
				} else {
					mm, machine, session, isOrg, err := orgID(args[0])
					if err != nil {
						return err
					}
					if isOrg && machine == mm.Machine {
						args[0] = session // this machine's own agent: deliver locally
					} else if isOrg {
						m, target = mm, pool.Selector{ID: args[0]}
					}
				}
				if !target.IsZero() {
					if len(files) > 0 {
						return fmt.Errorf("--file works only for agents on this machine; use `context-drop upload` for a link")
					}
					if wait && target.ID == "" {
						return fmt.Errorf("--wait across an org works for one agent at a time")
					}
					agent := callerAgent(cmd.Context())
					if wait {
						return orgSendWait(cmd, m, target.ID, text, from, agent, timeout)
					}
					return orgSend(cmd, m, target, text, from, agent)
				}
			}
			if org.set() {
				return fmt.Errorf("--machine and --cwd work with --all across an org")
			}
			text, err := withFiles(text, files)
			if err != nil {
				return err
			}
			env, providers, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			opts := agents.SendOptions{From: from}
			if me, err := agents.Self(cmd.Context(), env, idx, selfPID()); err == nil {
				opts.ReplyTo = me.ID
			}
			var targets []agents.Session
			if all {
				targets = withoutSelf(cmd.Context(), env, idx, topLevel(filter.apply(cmd.Context(), env, idx.Sessions)))
				if len(targets) == 0 {
					return errNoGroup
				}
			} else {
				target, err := idx.Resolve(args[0])
				if err != nil {
					return err
				}
				targets = []agents.Session{target}
			}
			type sent struct {
				got   agents.Session
				since time.Time
				prior int
			}
			var delivered []sent
			var failed int
			for _, target := range targets {
				since := time.Now().Add(-5 * time.Second)
				prior := 0
				if wait {
					prior = priorMatches(cmd.Context(), env, providers, idx, target, text, since)
				}
				got, err := agents.Send(cmd.Context(), env, providers, idx, target, text, opts)
				if err != nil {
					if !all {
						return err
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "failed to send to %s: %v\n", target.ID, err)
					failed++
					continue
				}
				if got.ID != target.ID {
					fmt.Fprintf(cmd.ErrOrStderr(), "relayed to %s through parent %s via %s\n", target.ID, got.ID, got.Delivery)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "sent to %s via %s\n", got.ID, got.Delivery)
				}
				delivered = append(delivered, sent{got, since, prior})
			}
			if wait {
				type reply struct {
					msgs  []agents.Message
					final agents.Session
					err   error
				}
				replies := make([]reply, len(delivered))
				ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				var wg sync.WaitGroup
				for i, d := range delivered {
					wg.Add(1)
					go func(i int, d sent) {
						defer wg.Done()
						msgs, final, err := agents.WaitForReply(ctx, newEnv, providers, d.got, agents.WaitOptions{
							Timeout: timeout, Sent: text, Since: d.since, PriorMatches: d.prior,
						})
						replies[i] = reply{msgs, final, err}
					}(i, d)
				}
				wg.Wait()
				for i, r := range replies {
					if r.err != nil {
						if !all {
							return r.err
						}
						fmt.Fprintf(cmd.ErrOrStderr(), "no reply from %s: %v\n", delivered[i].got.ID, r.err)
						failed++
						continue
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "%s is %s\n", r.final.ID, r.final.Status)
					if all {
						fmt.Fprintf(cmd.OutOrStdout(), "== %s\n", r.final.ID)
					}
					printMessages(cmd.OutOrStdout(), r.msgs)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d sessions did not get the message or reply", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&files, "file", nil, "attach a local file by its absolute path (repeatable)")
	cmd.Flags().StringVar(&from, "from", "", "name the sender when it is not an agent (an agent sending is named by its ID)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the reply and print it")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	cmd.Flags().BoolVar(&all, "all", false, "send to every live top-level session that matches the filters, except your own")
	filter.register(cmd, "with --all, only send to")
	org.register(cmd, "with --all, only send to")
	return cmd
}

// priorMatches counts messages in the receiving transcript that already look
// like text, so --wait does not mistake an earlier identical prompt for this one.
func priorMatches(ctx context.Context, env *agents.Env, providers []agents.Provider, idx agents.Index, target agents.Session, text string, since time.Time) int {
	recipient := target
	if target.Subagent && target.Agent == agents.Claude {
		if parent, ok := idx.Parent(target); ok {
			recipient = parent
		}
	}
	p, err := agents.ProviderFor(providers, recipient.Agent)
	if err != nil {
		return 0
	}
	msgs, err := p.Transcript(ctx, env, recipient)
	if err != nil {
		return 0
	}
	return agents.SentMatches(msgs, text, since)
}

func newSetupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up the summary model, the agent launcher, and the Pi extension",
		Long: "Walks through choosing the model `context-drop summarize` uses (and which environment variable holds\n" +
			"its key; the key itself is never saved), and how `context-drop start` opens agents. Saves the answers to\n" +
			"~/.context-drop/config.toml. `setup pi` installs only the Pi extension.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isInteractive(cmd) {
				return fmt.Errorf("setup is interactive; run it in a terminal, or edit ~/.context-drop/config.toml (see docs/cli.md)")
			}
			return runSetup(cmd, newTermUI(cmd))
		},
	}
	var printOnly bool
	pi := &cobra.Command{
		Use:   "pi",
		Short: "Install the Pi extension that lets context-drop list and message live Pi sessions",
		Long: "Writes the Context Drop extension to ~/.pi/agent/extensions/context-drop.ts. Pi sessions started after\n" +
			"this publish themselves to ~/.context-drop/pi and accept messages from `context-drop send`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printOnly {
				_, err := cmd.OutOrStdout().Write(agents.PiExtension)
				return err
			}
			env, err := newEnv()
			if err != nil {
				return err
			}
			path := env.PiExtensionPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, agents.PiExtension, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s; restart running pi sessions to load it\n", path)
			return nil
		},
	}
	pi.Flags().BoolVar(&printOnly, "print", false, "print the extension instead of installing it")
	cmd.AddCommand(pi)
	return cmd
}

func lastMessages(msgs []agents.Message, n int) []agents.Message {
	if n > 0 && len(msgs) > n {
		return msgs[len(msgs)-n:]
	}
	return msgs
}

func printMessages(w io.Writer, msgs []agents.Message) {
	if len(msgs) == 0 {
		fmt.Fprintln(w, "(no messages)")
		return
	}
	for i, m := range msgs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		stamp := ""
		if !m.Time.IsZero() {
			stamp = " · " + m.Time.Local().Format("Jan 2 15:04")
		}
		fmt.Fprintf(w, "── %s%s\n%s\n", m.Role, stamp, m.Text)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func label(s agents.Session) string {
	parts := []string{}
	if s.Name != "" {
		parts = append(parts, s.Name)
	}
	if s.Title != "" && s.Title != s.Name {
		parts = append(parts, s.Title)
	}
	return truncateRunes(strings.Join(strings.Fields(strings.Join(parts, " — ")), " "), 70)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// withFiles lists local files under a message so the recipient can read them
// in place. Every file must exist; paths are made absolute.
func withFiles(text string, files []string) (string, error) {
	if len(files) == 0 {
		return text, nil
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\nFiles (on this machine; read them in place):")
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("--file %s: %w", f, err)
		}
		if info.IsDir() {
			fmt.Fprintf(&b, "\n- %s/ (directory)", abs)
		} else {
			fmt.Fprintf(&b, "\n- %s (%d bytes)", abs, info.Size())
		}
	}
	return b.String(), nil
}
