package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"contextdrop.dev/context-drop/internal/localhome"
	"contextdrop.dev/context-drop/internal/pool"
	"github.com/spf13/cobra"
)

// membership is this machine's org membership; a seam for tests.
var membership = func() (pool.Membership, error) {
	root, err := localhome.Root()
	if err != nil {
		return pool.Membership{}, err
	}
	return pool.LoadMembership(root)
}

// joined returns the membership, or ok=false when this machine has not joined an org.
func joined() (pool.Membership, bool, error) {
	m, err := membership()
	if errors.Is(err, pool.ErrNotJoined) {
		return m, false, nil
	}
	return m, err == nil, err
}

// orgID splits an org agent ID when this machine has joined an org.
func orgID(target string) (m pool.Membership, machine, session string, ok bool, err error) {
	machine, session, ok = pool.SplitID(target)
	if !ok {
		return m, "", "", false, nil
	}
	m, isJoined, err := joined()
	if err != nil || !isJoined {
		return m, "", "", false, err
	}
	return m, machine, session, true, nil
}

func newJoinCommand() *cobra.Command {
	var server, name string
	cmd := &cobra.Command{
		Use:   "join TOKEN",
		Short: "Add this machine to an org, so its agents join the org's pool",
		Long: "Exchange an org's join token for a credential for this machine, saved in ~/.context-drop/org.json.\n" +
			"Then run `context-drop daemon` so the org can see and reach this machine's agents. Once joined, list,\n" +
			"read, and send work across the org, with agent IDs like <machine>/<session>.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if m, isJoined, err := joined(); err != nil {
				return err
			} else if isJoined {
				return fmt.Errorf("this machine already joined org %s as %s; run `context-drop leave` first", m.Org, m.Machine)
			}
			if name == "" {
				host, err := os.Hostname()
				if err != nil {
					return err
				}
				name = pool.MachineName(host)
			}
			if !pool.ValidMachineName(name) {
				return fmt.Errorf("--name %q: use lowercase letters, digits, and dashes", name)
			}
			m, err := pool.Join(cmd.Context(), nil, server, strings.TrimSpace(args[0]), name)
			if err != nil {
				return err
			}
			root, err := localhome.Ensure()
			if err != nil {
				return err
			}
			if err := pool.SaveMembership(root, m); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Joined org %s as %s.\nRun `context-drop daemon` (and keep it running) so the org can see and reach this machine's agents.\n", m.Org, m.Machine)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", pool.DefaultServer, "the Context Drop server")
	cmd.Flags().StringVar(&name, "name", "", "this machine's name in the org (default: from the hostname)")
	return cmd
}

func newLeaveCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "leave",
		Short: "Remove this machine from its org and revoke its credential",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := membership()
			if err != nil {
				return err
			}
			if err := (pool.Client{Membership: m}).Leave(cmd.Context()); err != nil {
				if !force {
					return fmt.Errorf("%w (use --force to forget the org on this machine anyway)", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: the server did not confirm: %v\n", err)
			}
			root, err := localhome.Root()
			if err != nil {
				return err
			}
			if err := os.Remove(pool.MembershipPath(root)); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Left org %s.\n", m.Org)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "forget the org here even if the server cannot be reached")
	return cmd
}

func newDaemonCommand() *cobra.Command {
	var share []string
	var shareAll, readOnly bool
	cmd := &cobra.Command{
		Use:   "daemon (--share DIR... | --share-all) [--read-only]",
		Short: "Keep this machine connected to its org",
		Long: "Connect to the org's server, report this machine's shared agents every few seconds, and answer read and\n" +
			"send for them. Runs until interrupted, reconnecting when the connection drops.\n\n" +
			"Anyone in the org can read a shared agent's transcript and, unless --read-only, send it instructions it\n" +
			"may act on with its permissions. Share only what the org should reach: --share DIR (repeatable) shares\n" +
			"agents working in DIR or below it, --share-all shares every agent on this machine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(share) == 0 && !shareAll {
				return fmt.Errorf("choose what the org can reach: --share DIR (repeatable) or --share-all")
			}
			if len(share) > 0 && shareAll {
				return fmt.Errorf("use --share or --share-all, not both")
			}
			dirs, err := shareDirs(share)
			if err != nil {
				return err
			}
			m, err := membership()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			if shareAll {
				log.Info("sharing every agent on this machine", "read_only", readOnly)
			} else {
				log.Info("sharing agents working in", "dirs", dirs, "read_only", readOnly)
			}
			local := pool.Shared{Local: pool.Machine{Name: m.Machine}, All: shareAll, Dirs: dirs, ReadOnly: readOnly}
			return pool.Daemon{Membership: m, Local: local, Log: log}.Run(ctx)
		},
	}
	cmd.Flags().StringArrayVar(&share, "share", nil, "share agents working in this directory or below it (repeatable)")
	cmd.Flags().BoolVar(&shareAll, "share-all", false, "share every agent on this machine")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "let the org read shared agents but not send to them")
	return cmd
}

// shareDirs resolves the shared directories, keeping both the path as given
// and its symlink-free form so an agent's working directory matches either.
func shareDirs(in []string) ([]string, error) {
	var out []string
	for _, d := range in {
		path := d
		if path == "~" || strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("--share %s: %w", d, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("--share %s: not a directory", d)
		}
		out = append(out, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			out = append(out, real)
		}
	}
	return out, nil
}

// orgFlags are the selector flags list and send take once a machine has joined an org.
type orgFlags struct {
	machine, cwd string
	local        bool
}

func (f *orgFlags) register(cmd *cobra.Command, verb string) {
	cmd.Flags().StringVar(&f.machine, "machine", "", verb+" agents on this machine of the org")
	cmd.Flags().StringVar(&f.cwd, "cwd", "", verb+" agents working in this directory or below it")
	cmd.Flags().BoolVar(&f.local, "local", false, "only this machine's sessions, with the local IDs (as before joining an org)")
}

func (f orgFlags) set() bool { return f.machine != "" || f.cwd != "" }

// selector builds the org selector, refusing filters only local mode has.
func (f orgFlags) selector(filter sessionFilter) (pool.Selector, error) {
	if filter.byPlace() {
		return pool.Selector{}, fmt.Errorf("--in, --session, --window, and --pane work with --local only for now")
	}
	sel := pool.Selector{Machine: f.machine, Kind: strings.ToLower(filter.agent)}
	if f.cwd != "" {
		abs, err := filepath.Abs(f.cwd)
		if err != nil {
			return sel, err
		}
		sel.Cwd = abs
	}
	return sel, nil
}

func orgList(cmd *cobra.Command, m pool.Membership, sel pool.Selector, topLevelOnly, asJSON bool) error {
	list, err := (pool.Client{Membership: m}).List(cmd.Context(), sel)
	if err != nil {
		return err
	}
	if topLevelOnly {
		kept := list[:0]
		for _, a := range list {
			if a.Role == pool.RoleMain {
				kept = append(kept, a)
			}
		}
		list = kept
	}
	if asJSON {
		if list == nil {
			list = []pool.Agent{}
		}
		return writeJSON(cmd.OutOrStdout(), list)
	}
	if len(list) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No agents in org %s.\n", m.Org)
		return nil
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGENT\tSTATUS\tACTIVE\tCWD\tNAME\tWHERE")
	for _, a := range list {
		id := a.ID
		if a.Role != pool.RoleMain {
			id = "└ " + id
		}
		active, _ := time.Parse(time.RFC3339Nano, a.LastActive)
		cwd := a.Cwd
		if a.Machine == m.Machine {
			cwd = shortPath(cwd)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, a.Kind, a.Status, ago(active), cwd,
			truncateRunes(strings.Join(strings.Fields(a.Name), " "), 70), firstNonEmptyString(a.LocationLabel, "-"))
	}
	return w.Flush()
}

// orgRead reads an org agent: this machine's directly, others through the server.
func orgRead(ctx context.Context, m pool.Membership, machine, session string, opts pool.ReadOptions) (pool.ReadResult, error) {
	if machine == m.Machine {
		return pool.Machine{Name: m.Machine}.Read(ctx, pool.LocalRead{ID: session, ReadOptions: opts})
	}
	return (pool.Client{Membership: m}).Read(ctx, machine+"/"+session, opts)
}

func printEntries(w io.Writer, entries []pool.Entry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "(no entries)")
		return
	}
	for i, e := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		stamp := ""
		if t, err := time.Parse(time.RFC3339Nano, e.Time); err == nil {
			stamp = " · " + t.Local().Format("Jan 2 15:04")
		}
		switch e.Type {
		case "compacted":
			fmt.Fprintf(w, "── compacted (%s)%s\nThe conversation was compacted; what came before was summarized.\n", e.Trigger, stamp)
		case "tool":
			fmt.Fprintf(w, "── tool%s\n%s %s\n", stamp, e.Name, e.Input)
		default:
			fmt.Fprintf(w, "── %s%s\n%s\n", e.Type, stamp, e.Text)
		}
	}
}

// orgSend sends to the selected org agents and reports each result.
func orgSend(cmd *cobra.Command, m pool.Membership, target pool.Selector, text, from, agent string) error {
	out, err := (pool.Client{Membership: m}).Send(cmd.Context(), pool.SendRequest{Target: target, Text: text, From: from, Agent: agent})
	if err != nil {
		return err
	}
	failed := 0
	for _, r := range out.Results {
		if r.Error != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to send to %s: %s\n", r.ID, r.Error.Message)
			failed++
			continue
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "sent to %s via %s\n", r.ID, r.Via)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d agents did not get the message", failed, len(out.Results))
	}
	return nil
}

// callerAgent is the ID of the agent session running this command, or "" when
// a person runs it.
func callerAgent(ctx context.Context) string {
	env, _, idx, err := discover(ctx)
	if err != nil {
		return ""
	}
	me, err := agents.Self(ctx, env, idx, selfPID())
	if err != nil {
		return ""
	}
	return me.ID
}

// orgWaitEvery is how often orgSendWait reads the agent.
var orgWaitEvery = 2 * time.Second

// orgSendWait sends to one org agent, then reads it until it has answered and
// settled, and prints the answer.
func orgSendWait(cmd *cobra.Command, m pool.Membership, id, text, from, agent string, timeout time.Duration) error {
	c := pool.Client{Membership: m}
	before, err := c.Read(cmd.Context(), id, pool.ReadOptions{Last: 1})
	if err != nil {
		return err
	}
	if err := orgSend(cmd, m, pool.Selector{ID: id}, text, from, agent); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	unchanged, last := 0, -1
	for {
		res, err := c.Read(ctx, id, pool.ReadOptions{All: true, After: before.Cursor})
		var perr *pool.Error
		if errors.As(err, &perr) && perr.Code == "not_found" {
			return fmt.Errorf("%s is no longer live", id)
		}
		if err == nil {
			if len(res.Entries) == last {
				unchanged++
			} else {
				unchanged, last = 0, len(res.Entries)
			}
			status := res.Agent.Status
			settled := status == "idle" || status == "blocked" || (status == "unknown" && unchanged >= 3)
			if reply, ok := pool.Reply(res.Entries, text); ok && settled {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s is %s\n", id, status)
				printEntries(cmd.OutOrStdout(), reply)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for %s to answer", timeout, id)
		case <-time.After(orgWaitEvery):
		}
	}
}
