package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/daemon"
	"contextdrop.dev/context-drop/internal/imessage"
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
		for _, worker := range st.Workers {
			fmt.Fprintf(cmd.OutOrStdout(), "Worker %d: %s (%s, %s %s), queued=%d\n", worker.Worker, worker.Status, worker.Agent, worker.Backend, worker.PaneID, worker.Queued)
		}
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
	var name, repo, prompt, promptFile, cron, timezone string
	var every time.Duration
	var disabled, silent bool
	add := &cobra.Command{Use: "add", Short: "Add or update a scheduled worker prompt", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
		if repo == "" {
			var err error
			repo, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		store, err := orchestrator.NewStore()
		if err != nil {
			return err
		}
		s := orchestrator.Schedule{Name: name, Type: orchestrator.ScheduleAgent, Agent: "codex", Repo: repo, Prompt: prompt, Every: every, Cron: cron, Timezone: timezone, Enabled: !disabled, Silent: silent, Overlap: orchestrator.OverlapSkip, MissedRunPolicy: "latest"}
		if err := store.Update(func(st *orchestrator.State) error { return orchestrator.Upsert(st, s, time.Now().UTC()) }); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "saved local schedule %s (%s)\n", name, cadenceOf(s))
		return nil
	}}
	add.Flags().StringVar(&name, "name", "", "stable schedule name")
	add.Flags().StringVar(&repo, "repo", "", "absolute task directory (default: current directory)")
	add.Flags().StringVar(&prompt, "prompt", "", "task prompt")
	add.Flags().StringVar(&promptFile, "prompt-file", "", "read and snapshot prompt from file")
	add.Flags().DurationVar(&every, "every", 0, "interval, minimum 1m")
	add.Flags().StringVar(&cron, "cron", "", "five-field calendar schedule")
	add.Flags().StringVar(&timezone, "timezone", "", "IANA timezone for --cron")
	add.Flags().BoolVar(&disabled, "disabled", false, "save paused")
	add.Flags().BoolVar(&silent, "silent", false, "keep routine reports internal; still deliver questions and failures")
	add.MarkFlagsMutuallyExclusive("every", "cron")
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
		latest := map[string]orchestrator.Job{}
		for _, job := range st.Jobs {
			latest[job.ScheduleName] = job
		}
		for _, s := range st.Schedules {
			cadence := s.Every.String()
			if s.Cron != "" {
				cadence = fmt.Sprintf("%s (%s)", s.Cron, s.Timezone)
			}
			jobStatus, deliveryStatus := "none", "n/a"
			if job, ok := latest[s.Name]; ok {
				jobStatus = job.Status
				if job.DeliveryStatus != "" {
					deliveryStatus = job.DeliveryStatus
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\ttype=%s\toverlap=%s\tbackend=%s\tagent=%s\ttarget=%s\tenabled=%t\tsilent=%t\tfailures=%d\tjob=%s\tdelivery=%s\tnext=%s\n", s.Name, cadence, s.Type, s.Overlap, s.Backend, s.Agent, s.WatchPane+s.WatchTarget, s.Enabled, s.Silent, s.ConsecutiveFailures, jobStatus, deliveryStatus, s.NextRunAt.Format(time.RFC3339))
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
	run := &cobra.Command{Use: "run-now <name>", Aliases: []string{"run"}, Short: "Run one configured schedule immediately", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runScheduleOnce(cmd.Context(), cmd, args[0]) }}
	setEnabled := func(enabled bool) *cobra.Command {
		verb := "pause"
		if enabled {
			verb = "resume"
		}
		return &cobra.Command{Use: verb + " <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			store, e := orchestrator.NewStore()
			if e != nil {
				return e
			}
			return store.Update(func(st *orchestrator.State) error {
				return orchestrator.SetEnabled(st, args[0], enabled, time.Now().UTC())
			})
		}}
	}
	root.AddCommand(add, list, remove, run, setEnabled(false), setEnabled(true))
	root.Flags().BoolVar(&silent, "silent", false, "keep routine reports internal; still deliver questions and failures")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("silent") {
			if len(args) != 1 {
				return fmt.Errorf("usage: context-drop schedule <name> --silent[=false]")
			}
			store, err := orchestrator.NewStore()
			if err != nil {
				return err
			}
			return store.Update(func(st *orchestrator.State) error {
				for i := range st.Schedules {
					if st.Schedules[i].Name == args[0] {
						st.Schedules[i].Silent = silent
						fmt.Fprintf(cmd.OutOrStdout(), "schedule %s silent=%t\n", args[0], silent)
						return nil
					}
				}
				return fmt.Errorf("schedule %q not found", args[0])
			})
		}
		switch {
		case len(args) == 0:
			return cmd.Help()
		case len(args) == 1:
			return showSchedulePrompt(cmd, args[0])
		case len(args) == 2:
			return setSchedulePrompt(cmd, args[0], args[1])
		default:
			return fmt.Errorf("usage: context-drop schedule <name> [prompt]; pass - as the prompt to read it from stdin")
		}
	}
	return root
}

// showSchedulePrompt prints one schedule's exact stored prompt.
func showSchedulePrompt(cmd *cobra.Command, name string) error {
	store, err := orchestrator.NewStore()
	if err != nil {
		return err
	}
	st, err := store.Load()
	if err != nil {
		return err
	}
	for _, s := range st.Schedules {
		if s.Name == name {
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n---\n%s\n---\n", s.Name, cadenceOf(s), s.Prompt)
			return nil
		}
	}
	return fmt.Errorf("schedule %q not found", name)
}

// setSchedulePrompt replaces one existing schedule's prompt in place, keeping
// every other field (cadence, agent, backend, repo) untouched.
func setSchedulePrompt(cmd *cobra.Command, name, prompt string) error {
	if prompt == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		prompt = string(data)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("prompt must not be empty")
	}
	store, err := orchestrator.NewStore()
	if err != nil {
		return err
	}
	if err := store.Update(func(st *orchestrator.State) error {
		for i := range st.Schedules {
			if st.Schedules[i].Name != name {
				continue
			}
			st.Schedules[i].Prompt = prompt
			return nil
		}
		return fmt.Errorf("schedule %q not found; use schedule add to create one", name)
	}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "updated prompt for schedule %s\n", name)
	return nil
}

func cadenceOf(s orchestrator.Schedule) string {
	if s.Cron != "" {
		return fmt.Sprintf("cron %s (%s)", s.Cron, s.Timezone)
	}
	return s.Every.String()
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
	runner := daemon.Runner{Store: store, Notifier: orchestrator.LocalNotifier{}, Now: func() time.Time { return time.Now().UTC() }}
	var tasks []runtimeclient.ManagedTask
	failReserved := func(runErr error) error {
		_ = store.Update(func(st *orchestrator.State) error {
			return orchestrator.SetJobStatus(st, job.ID, "failed", "", runErr.Error(), time.Now().UTC())
		})
		return runErr
	}
	if selected.Type != orchestrator.ScheduleCommand {
		client, clientErr := runtimeclient.New()
		if clientErr != nil {
			return failReserved(clientErr)
		}
		runner.Runtime = client
		imsgCfg, loadErr := imessage.Load()
		if loadErr != nil {
			return failReserved(loadErr)
		}
		runner.IMessage = &imessage.Adapter{Config: imsgCfg}
	}
	runner.ExecuteClaim(ctx, orchestrator.Claim{Schedule: selected, Job: job}, tasks, time.Now().UTC())
	st, err := store.Load()
	if err != nil {
		return err
	}
	for _, j := range st.Jobs {
		if j.ID == job.ID {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", j.ID, j.Status)
			if j.Status == "failed" || j.Status == "timed_out" {
				return fmt.Errorf("schedule %s: %s", selected.Name, j.Error)
			}
			return nil
		}
	}
	return fmt.Errorf("job %s disappeared", job.ID)
}
