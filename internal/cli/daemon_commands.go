package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/daemon"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
	"github.com/spf13/cobra"
)

func newDaemonCommand() *cobra.Command {
	root := &cobra.Command{Use: "daemon", Short: "Manage the Context Drop local orchestration daemon"}
	run := &cobra.Command{Use: "run", Short: "Run the daemon in the foreground", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return daemon.Run(ctx)
	}}
	start := &cobra.Command{Use: "start", Short: "Start the daemon in the background", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		info, err := daemon.StartBackground()
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Context Drop daemon running (pid %d)\n", info.PID)
		}
		return err
	}}
	stop := &cobra.Command{Use: "stop", Short: "Stop the background daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := daemon.StopBackground(); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Context Drop daemon stopped")
		return nil
	}}
	restart := &cobra.Command{Use: "restart", Short: "Restart the background daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := daemon.StopBackground(); err != nil {
			return err
		}
		info, err := daemon.StartBackground()
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Context Drop daemon running (pid %d)\n", info.PID)
		}
		return err
	}}
	var jsonOut bool
	status := &cobra.Command{Use: "status", Short: "Show daemon, runtime, schedules, messaging, and runs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		st, err := daemon.CurrentStatus(cmd.Context())
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Daemon: %t (pid %d)\nRuntime: %t\nService: installed=%t loaded=%t\nSchedules: %d enabled, %d total; jobs: %d\n", st.Alive, st.PID, st.RuntimeHealthy, st.Installed, st.Loaded, st.EnabledScheduleCount, st.ScheduleCount, st.JobCount)
		fmt.Fprintf(cmd.OutOrStdout(), "iMessage: configured=%t enabled=%t initialized=%t\n", st.IMessageConfigured, st.IMessageEnabled, st.IMessageInitialized)
		if st.LastMessagePollAt != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Last iMessage poll: %s\n", st.LastMessagePollAt.Format(time.RFC3339))
		}
		if st.LastMessageError != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "iMessage error: %s\n", st.LastMessageError)
		}
		if st.LastRuntimeError != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Runtime error: %s\n", st.LastRuntimeError)
		}
		return nil
	}}
	status.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	var lines int
	logs := &cobra.Command{Use: "logs", Short: "Print daemon log output", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		text, err := daemon.ReadLogs(lines)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no Context Drop daemon log exists yet")
		}
		if err == nil {
			fmt.Fprint(cmd.OutOrStdout(), text)
		}
		return err
	}}
	logs.Flags().IntVar(&lines, "lines", 100, "number of trailing lines (0 prints all)")
	install := &cobra.Command{Use: "install", Short: "Install and load the per-user background service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := daemon.InstallService(false); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Context Drop daemon service installed")
		return nil
	}}
	uninstall := &cobra.Command{Use: "uninstall", Short: "Unload and remove the per-user background service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := daemon.UninstallService(false); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Context Drop daemon service uninstalled")
		return nil
	}}
	watchdog := newWatchdogCommand()
	root.AddCommand(run, start, stop, restart, status, logs, install, uninstall, watchdog)
	return root
}

func newWatchdogCommand() *cobra.Command {
	root := &cobra.Command{Use: "watchdog", Short: "Manage the daemon availability watchdog"}
	root.AddCommand(
		&cobra.Command{Use: "install", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := daemon.InstallService(true); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Context Drop watchdog installed")
			return nil
		}},
		&cobra.Command{Use: "uninstall", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := daemon.UninstallService(true); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Context Drop watchdog uninstalled")
			return nil
		}},
		&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(daemon.WatchdogStatus())
		}},
	)
	check := &cobra.Command{Use: "check", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return daemon.WatchdogCheck() }}
	root.AddCommand(check)
	return root
}

func newScheduleCommand() *cobra.Command {
	root := &cobra.Command{Use: "schedule", Short: "Manage durable local agent schedules"}
	var name, agent, backend, repo, prompt, promptFile, cron, timezone string
	var every time.Duration
	var notify, disabled bool
	add := &cobra.Command{Use: "add", Short: "Add or update an interval or calendar schedule", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if prompt != "" && promptFile != "" {
			return fmt.Errorf("use either --prompt or --prompt-file")
		}
		if promptFile != "" {
			data, err := os.ReadFile(promptFile)
			if err != nil {
				return err
			}
			prompt = string(data)
		}
		cfg, err := runtimeclient.LoadConfig()
		if err != nil {
			return fmt.Errorf("load local runtime configuration: %w", err)
		}
		_, found := cfg.Agents[agent]
		if !found {
			return fmt.Errorf("agent %q is not configured in the local runtime", agent)
		}
		if backend != "" && backend != "tmux" && backend != "herdr" {
			return fmt.Errorf("--backend must be tmux or herdr")
		}
		store, err := orchestrator.NewStore()
		if err != nil {
			return err
		}
		s := orchestrator.Schedule{Name: name, Agent: agent, Backend: backend, Repo: repo, Prompt: prompt, Every: every, Cron: cron, Timezone: timezone, Enabled: !disabled, NotifyOnInitiate: notify}
		if err := store.Update(func(st *orchestrator.State) error {
			return orchestrator.Upsert(st, s, time.Now().UTC())
		}); err != nil {
			return err
		}
		cadence := every.String()
		if cron != "" {
			cadence = fmt.Sprintf("cron %q in %s", cron, timezone)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "saved local schedule %s (%s)\n", name, cadence)
		return nil
	}}
	add.Flags().StringVar(&name, "name", "", "stable schedule name")
	add.Flags().StringVar(&agent, "agent", "", "configured local agent")
	add.Flags().StringVar(&backend, "backend", "", "session backend: tmux or herdr (default from runtime config)")
	add.Flags().StringVar(&repo, "repo", "", "absolute local repository path")
	add.Flags().StringVar(&prompt, "prompt", "", "prompt text snapshot")
	add.Flags().StringVar(&promptFile, "prompt-file", "", "read and snapshot prompt from file")
	add.Flags().DurationVar(&every, "every", 0, "interval such as 15m or 2h (minimum 1m)")
	add.Flags().StringVar(&cron, "cron", "", "exact five-field calendar schedule")
	add.Flags().StringVar(&timezone, "timezone", "", "IANA timezone for --cron, such as America/Los_Angeles")
	add.MarkFlagsMutuallyExclusive("every", "cron")
	add.Flags().BoolVar(&notify, "notify", false, "send a local notification when a run is initiated")
	add.Flags().BoolVar(&disabled, "disabled", false, "save disabled")
	var jsonOut bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		store, e := orchestrator.NewStore()
		if e != nil {
			return e
		}
		st, e := store.Load()
		if e != nil {
			return e
		}
		if jsonOut {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"schedules": st.Schedules, "jobs": st.Jobs})
		}
		for _, s := range st.Schedules {
			cadence := s.Every.String()
			if s.Cron != "" {
				cadence = fmt.Sprintf("%s (%s)", s.Cron, s.Timezone)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\tenabled=%t\tnext=%s\n", s.Name, cadence, s.Backend, s.Agent, s.Repo, s.Enabled, s.NextRunAt.Format(time.RFC3339))
		}
		return nil
	}}
	list.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	remove := &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, e := orchestrator.NewStore()
		if e != nil {
			return e
		}
		e = store.Update(func(st *orchestrator.State) error {
			if !orchestrator.Remove(st, args[0]) {
				return fmt.Errorf("schedule %q not found", args[0])
			}
			return nil
		})
		if e == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "removed schedule %s\n", args[0])
		}
		return e
	}}
	run := &cobra.Command{Use: "run <name>", Short: "Launch one configured schedule immediately", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runScheduleOnce(cmd.Context(), cmd, args[0]) }}
	root.AddCommand(add, list, remove, run)
	return root
}

func runScheduleOnce(ctx context.Context, cmd *cobra.Command, name string) error {
	store, err := orchestrator.NewStore()
	if err != nil {
		return err
	}
	var selected orchestrator.Schedule
	var job orchestrator.Job
	// Snapshot the schedule and persist a launching reservation in one locked
	// transaction. Later remove/update operations cannot change this explicit
	// manual invocation, and its outcome is attached to the reserved job.
	if err := store.Update(func(st *orchestrator.State) error {
		var claimErr error
		selected, job, claimErr = orchestrator.ClaimManual(st, name, time.Now().UTC())
		return claimErr
	}); err != nil {
		return err
	}
	client, err := runtimeclient.New()
	if err == nil {
		var run runtimeclient.Run
		run, err = client.Launch(ctx, selected.Agent, selected.Repo, selected.Prompt, "schedule-"+selected.Name, selected.Backend, "")
		if err == nil {
			err = store.Update(func(st *orchestrator.State) error {
				return orchestrator.CompleteJob(st, job.ID, "launched", run.ID, "")
			})
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", run.ID, run.Status)
			}
			return err
		}
	}
	launchErr := err
	if updateErr := store.Update(func(st *orchestrator.State) error {
		return orchestrator.CompleteJob(st, job.ID, "launch_error", "", launchErr.Error())
	}); updateErr != nil {
		return updateErr
	}
	return launchErr
}
