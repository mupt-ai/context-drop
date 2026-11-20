package cli

import (
	"errors"
	"fmt"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"github.com/spf13/cobra"
)

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
