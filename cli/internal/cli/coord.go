package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			var prev []agents.Session
			poll := func() ([]agents.Session, bool, error) {
				env, _, idx, err := discover(ctx)
				if err != nil {
					return nil, false, err
				}
				if _, err := env.Procs(ctx); err != nil || len(idx.Warnings) > 0 || ctx.Err() != nil {
					return nil, false, nil
				}
				sessions := keepUnlocated(filter.apply(ctx, env, idx.Sessions), prev, idx, filter.byPlace())
				if !subagents {
					sessions = topLevel(sessions)
				}
				return sessions, ctx.Err() == nil, nil
			}
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

// keepUnlocated keeps a previously matched session that is still live but
// whose pane could not be found this time (a multiplexer listing failed), so
// a place filter does not report it as ended and then started again.
func keepUnlocated(matched, prev []agents.Session, idx agents.Index, byPlace bool) []agents.Session {
	if !byPlace {
		return matched
	}
	in := map[string]bool{}
	for _, s := range matched {
		in[s.ID] = true
	}
	for _, p := range prev {
		if in[p.ID] {
			continue
		}
		for _, s := range idx.Sessions {
			if s.ID == p.ID && s.Location == nil {
				s.Location = p.Location
				matched = append(matched, s)
			}
		}
	}
	return matched
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
