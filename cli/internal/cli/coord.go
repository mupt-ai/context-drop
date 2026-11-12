package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"github.com/spf13/cobra"
)

func newWatchCommand() *cobra.Command {
	var filter sessionFilter
	var interval, duration time.Duration
	var asJSON, subagents bool
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Print sessions as they start, end, or change status",
		Long: "Poll live sessions and print one line whenever a session starts, ends, or changes status (idle, working,\n" +
			"blocked, unknown). It runs until interrupted, or for --for. --json prints one JSON object per change.\n" +
			"Subagents are left out unless --subagents is given.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, duration)
				defer cancel()
			}
			// poll reports ok=false for a pass that may have missed sessions: a
			// provider failed, the process table could not be read, or time ran
			// out mid-pass. Such a pass is skipped rather than shown as sessions ending.
			poll := func() ([]agents.Session, bool, error) {
				env, _, idx, err := discover(ctx)
				if err != nil {
					return nil, false, err
				}
				if _, err := env.Procs(ctx); err != nil || len(idx.Warnings) > 0 || ctx.Err() != nil {
					return nil, false, nil
				}
				sessions := filter.apply(ctx, env, idx.Sessions)
				if !subagents {
					sessions = topLevel(sessions)
				}
				return sessions, ctx.Err() == nil, nil
			}
			var prev []agents.Session
			for ok := false; !ok; {
				var err error
				if prev, ok, err = poll(); err != nil {
					return err
				}
				if !ok {
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(interval):
					}
				}
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "watching %d sessions every %s\n", len(prev), interval)
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
				next, ok, err := poll()
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				if !ok {
					continue
				}
				for _, c := range agents.Changes(prev, next, time.Now()) {
					if asJSON {
						if err := writeJSONLine(cmd, c); err != nil {
							return err
						}
						continue
					}
					fmt.Fprintln(cmd.OutOrStdout(), describeChange(c))
				}
				prev = next
			}
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "how often to check")
	cmd.Flags().DurationVar(&duration, "for", 0, "stop after this long (default: until interrupted)")
	cmd.Flags().BoolVar(&subagents, "subagents", false, "also report subagents")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per change")
	filter.register(cmd, "only watch")
	return cmd
}

func describeChange(c agents.Change) string {
	what := ""
	switch c.Event {
	case agents.ChangeStarted:
		what = "started (" + c.To + ")"
	case agents.ChangeEnded:
		what = "ended (was " + c.From + ")"
	default:
		what = c.From + " → " + c.To
	}
	line := fmt.Sprintf("%s  %-12s %-8s %-22s %s", c.Time.Local().Format("15:04:05"), c.ID, c.Agent, what, shortPath(c.Session.Cwd))
	if name := label(c.Session); name != "" {
		line += "  " + truncateRunes(name, 40)
	}
	return line
}

func writeJSONLine(cmd *cobra.Command, v any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
}

func newStopCommand() *cobra.Command {
	var filter sessionFilter
	var all, force bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stop [TARGET...]",
		Short: "Stop live agent sessions",
		Long: "Stop the named sessions, or with --all every live top-level session that matches the filters (never the\n" +
			"session running this command).\n\n" +
			"A session in a pane that `context-drop start` opened for it has that pane closed. Any other session gets\n" +
			"SIGTERM, so a pane you opened yourself keeps its shell. A process that serves several sessions (a Codex\n" +
			"app-server, `opencode serve`) is never signalled. Sessions that are working or blocked, and your own\n" +
			"session, are refused unless --force is given. Subagents cannot be stopped on their own.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 0) {
				return fmt.Errorf("name sessions to stop, or pass --all (with filters to narrow it)")
			}
			if filter.set() && !all {
				return fmt.Errorf("--agent, --in, --session, --window, and --pane need --all")
			}
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			me := selves(cmd.Context(), env, idx)
			var targets []agents.Session
			if all {
				targets = withoutSelf(cmd.Context(), env, idx, topLevel(filter.apply(cmd.Context(), env, idx.Sessions)))
				if len(targets) == 0 {
					return errNoGroup
				}
			} else {
				for _, arg := range args {
					s, err := idx.Resolve(arg)
					if err != nil {
						return err
					}
					targets = append(targets, s)
				}
			}
			// Stop every target first, then wait for all of them together.
			how := map[string]string{}
			failed := 0
			for _, s := range targets {
				did, err := stopOne(cmd, env, s, me[s.ID], force)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", s.ID, err)
					failed++
					continue
				}
				how[s.ID] = did
			}
			deadline := time.Now().Add(timeout)
			for len(how) > 0 {
				_, _, idx, err := discover(cmd.Context())
				if err != nil {
					return err
				}
				for _, s := range targets {
					if _, waiting := how[s.ID]; !waiting {
						continue
					}
					if _, err := idx.Resolve(s.ID); errors.Is(err, agents.ErrNotFound) {
						fmt.Fprintf(cmd.OutOrStdout(), "stopped %s (%s)\n", s.ID, how[s.ID])
						delete(how, s.ID)
					}
				}
				if len(how) == 0 {
					break
				}
				if time.Now().After(deadline) {
					for _, s := range targets {
						if did, waiting := how[s.ID]; waiting {
							fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s, but it is still running after %s\n", s.ID, did, timeout)
							failed++
						}
					}
					break
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(250 * time.Millisecond):
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d sessions were not stopped", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop every live top-level session that matches the filters")
	cmd.Flags().BoolVar(&force, "force", false, "also stop sessions that are working or blocked, or your own")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "how long to wait for each session to end")
	filter.register(cmd, "with --all, only stop")
	return cmd
}

// stopOne applies the guards and stops one session, returning what it did.
func stopOne(cmd *cobra.Command, env *agents.Env, s agents.Session, isSelf, force bool) (string, error) {
	if !force {
		switch {
		case isSelf:
			return "", fmt.Errorf("this may be your own session; pass --force to stop it")
		case s.Status == agents.StatusWorking || s.Status == agents.StatusBlocked:
			return "", fmt.Errorf("is %s; pass --force to stop it anyway", s.Status)
		}
	}
	return agents.Stop(cmd.Context(), env, s)
}

func newClaimCommand() *cobra.Command {
	var note, as string
	var force bool
	cmd := &cobra.Command{
		Use:   "claim TARGET...",
		Short: "Claim a path or task for your session so other agents can see it",
		Long: "Record that your session is working on TARGET: a file or directory (anything that exists, or contains a\n" +
			"slash, or starts with \".\") or else a task name. Claims are advisory: nothing is locked. They show in\n" +
			"`context-drop list` and `context-drop claims`, and they disappear when your session ends.\n\n" +
			"Your session is found from the processes above this command, so an agent can run it as is; pass --as to\n" +
			"name the session. A claim that overlaps another live session's claim (a path inside another, or the same\n" +
			"task name) is refused and nothing is recorded, unless --force is given. Paths in different worktrees of\n" +
			"one repository are compared by their path within the repository.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			me, err := claimant(cmd, env, idx, as)
			if err != nil {
				return err
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			var wanted []agents.Claim
			for _, target := range args {
				wanted = append(wanted, agents.NewClaim(me, target, dir, note, time.Now()))
			}
			if err := env.Claims().Add(idx, wanted, force); err != nil {
				var conflict agents.ClaimConflict
				if errors.As(err, &conflict) {
					return fmt.Errorf("%w\nnothing was claimed; coordinate with the holder, or pass --force to claim anyway", err)
				}
				return err
			}
			for _, c := range wanted {
				fmt.Fprintf(cmd.OutOrStdout(), "%s claimed %s\n", me.ID, claimLabel(c))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "what you are doing with it")
	cmd.Flags().StringVar(&as, "as", "", "the session to claim for (default: the one running this command)")
	cmd.Flags().BoolVar(&force, "force", false, "claim even if another session's claim overlaps")
	return cmd
}

func newReleaseCommand() *cobra.Command {
	var as string
	cmd := &cobra.Command{
		Use:   "release [TARGET...]",
		Short: "Release your session's claims (all of them when no TARGET is given)",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			me, err := claimant(cmd, env, idx, as)
			if err != nil {
				return err
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			var targets []agents.Claim
			for _, target := range args {
				targets = append(targets, agents.NewClaim(me, target, dir, "", time.Time{}))
			}
			released, err := env.Claims().Release(idx, me.ID, targets)
			if err != nil {
				return err
			}
			if len(released) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s had no matching claims\n", me.ID)
			}
			for _, c := range released {
				fmt.Fprintf(cmd.OutOrStdout(), "%s released %s\n", me.ID, claimLabel(c))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "the session to release for (default: the one running this command)")
	return cmd
}

func claimant(cmd *cobra.Command, env *agents.Env, idx agents.Index, as string) (agents.Session, error) {
	me, err := self(cmd.Context(), env, idx, as)
	if errors.Is(err, agents.ErrNoSelf) {
		return me, fmt.Errorf("this command is not running inside a live agent session; pass --as TARGET")
	}
	if err == nil && me.Subagent {
		parent, ok := idx.Parent(me)
		if !ok {
			return me, fmt.Errorf("%s is a subagent whose parent is not live", me.ID)
		}
		me = parent // claims belong to top-level sessions
	}
	return me, err
}

func claimLabel(c agents.Claim) string {
	label := c.Target
	if c.Path != "" {
		label = shortPath(c.Path)
	}
	if c.Note != "" {
		label += " (" + c.Note + ")"
	}
	return label
}

func newClaimsCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "List the claims of live sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			claims, err := env.Claims().Live(idx)
			if err != nil {
				return err
			}
			if asJSON {
				if claims == nil {
					claims = []agents.Claim{}
				}
				return writeJSON(cmd.OutOrStdout(), claims)
			}
			if len(claims) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No claims.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION\tAGENT\tCLAIM\tSINCE\tNOTE")
			for _, c := range claims {
				target := c.Target
				if c.Path != "" {
					target = shortPath(c.Path)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Session, c.Agent, target, ago(c.ClaimedAt), c.Note)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newOverlapsCommand() *cobra.Command {
	var asJSON bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "overlaps",
		Short: "Show where live sessions may be stepping on each other",
		Long: "Report live top-level sessions that share a git worktree, files edited by more than one session within\n" +
			"--since (in one worktree, or in different worktrees of one repository), and edits to files another\n" +
			"session has claimed. Edits come from transcripts: Claude Code's Edit/MultiEdit/Write/NotebookEdit,\n" +
			"Codex's apply_patch, and Pi's edit and write tools. OpenCode edits and shell commands that write files\n" +
			"are not seen.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, _, idx, err := discover(cmd.Context())
			if err != nil {
				return err
			}
			claims, err := env.Claims().Live(idx)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: claims: %v\n", err)
			}
			overlaps := agents.FindOverlaps(idx.Sessions, claims, time.Now().Add(-since))
			if asJSON {
				if overlaps == nil {
					overlaps = []agents.Overlap{}
				}
				return writeJSON(cmd.OutOrStdout(), overlaps)
			}
			if len(overlaps) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No overlaps.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "KIND\tPATH\tSESSIONS\tDETAIL")
			for _, o := range overlaps {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.Kind, shortPath(o.Path), strings.Join(o.Sessions, ", "), o.Detail)
			}
			return w.Flush()
		},
	}
	cmd.Flags().DurationVar(&since, "since", 2*time.Hour, "only count edits this recent")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
