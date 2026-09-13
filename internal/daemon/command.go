package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/orchestrator"
)

// Scripts are daemon jobs, not conversations. Keep their output in a job log.
func (r *Runner) executeCommand(ctx context.Context, claim orchestrator.Claim) {
	s, job := claim.Schedule, claim.Job
	runID := "local:" + job.ID
	if err := r.Store.Update(func(st *orchestrator.State) error {
		return orchestrator.SetJobStatus(st, job.ID, "running", runID, "", r.Now())
	}); err != nil {
		return
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := func() error {
		if len(s.Command) == 0 {
			return fmt.Errorf("script argv is empty")
		}
		dir := filepath.Join(filepath.Dir(r.Store.Path), "command-logs")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		output, err := os.OpenFile(filepath.Join(dir, filepath.Base(job.ID)+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer output.Close()
		cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = s.Cwd, output, output
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		return cmd.Run()
	}()
	status, detail := "completed", ""
	if err != nil {
		status, detail = "failed", err.Error()
	}
	if ctx.Err() == context.DeadlineExceeded {
		status, detail = "timed_out", "script exceeded its timeout"
	}
	_ = r.Store.Update(func(st *orchestrator.State) error {
		return orchestrator.SetJobStatus(st, job.ID, status, runID, detail, r.Now())
	})
}
