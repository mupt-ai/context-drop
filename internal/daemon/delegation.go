package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/runtimeclient"
)

// configureRouter gives the constrained router only its scoped capability. The
// runtime's general token remains in the Go client and is never placed in the
// Pi process environment.
func (r *Runner) configureRouter(ctx context.Context) error {
	client := r.Delegation
	if client == nil {
		return fmt.Errorf("delegation runtime is unavailable")
	}
	capability, err := client.DelegateCapability(ctx)
	if err != nil {
		return err
	}
	responder, ok := r.IMessage.PersistentResponder.(*imessage.PiRPCResponder)
	if !ok {
		return fmt.Errorf("router mode requires the persistent Pi RPC responder")
	}
	runtimeClient, ok := client.(*runtimeclient.Client)
	if !ok {
		return fmt.Errorf("delegation runtime does not expose a loopback address")
	}
	responder.SetDelegationEnv(strings.TrimRight(runtimeClient.Address, "/")+"/v1/delegate", capability, r.IMessage.Config.ChatID)
	return nil
}

func (r *Runner) DeliverReports(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.deliverReportsOnce(ctx)
		}
	}
}

func (r *Runner) deliverReportsOnce(ctx context.Context) {
	if r.IMessage == nil || !r.IMessage.Config.Enabled {
		return
	}
	client := r.Delegation
	if client == nil {
		return
	}
	reports, err := client.PendingReports(ctx)
	if err != nil {
		return
	}
	for _, pending := range reports {
		report, claimed, claimErr := client.DeliverReport(ctx, pending.ID)
		if claimErr != nil || !claimed {
			continue
		}
		message := formatReport(report)
		sendCtx, cancel := context.WithTimeout(ctx, time.Duration(r.IMessage.Config.SendTimeoutSeconds)*time.Second)
		if err := r.IMessage.Send(sendCtx, message); err != nil {
			// The report was atomically claimed before sending. This is an
			// intentional at-most-once policy: restart cannot duplicate a
			// notification after a send/crash race.
		}
		cancel()
	}
}

func (r *Runner) continueActiveTask(ctx context.Context, incoming string) (string, bool) {
	client := r.Delegation
	if client == nil {
		return "", false
	}
	tasks, err := client.Delegations(ctx)
	if err != nil {
		return "", false
	}
	var latest *runtimeclient.Delegation
	for i := range tasks {
		task := &tasks[i]
		if task.ChatID != r.IMessage.Config.ChatID || (latest != nil && task.CreatedAt <= latest.CreatedAt) {
			continue
		}
		latest = task
	}
	if latest == nil || latest.Status != "running" {
		return "", false
	}
	prompt := fmt.Sprintf("Continue active task %s safely.\n\nOriginal task:\n%s\n\nNew user message:\n%s", latest.RunID, latest.Task, incoming)
	if len(prompt) > 16000 {
		prompt = prompt[:16000]
	}
	run, err := client.Delegate(ctx, prompt, r.IMessage.Config.ChatID)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("got it — i started continuation worker %s. updates will come back here.", run.ID), true
}

func formatReport(report runtimeclient.ParentReport) string {
	kind := map[string]string{"started": "started", "progress": "progress", "needs_user": "needs your input", "completed": "done", "failed": "failed"}[report.Kind]
	if kind == "" {
		kind = "update"
	}
	message := strings.TrimSpace(report.Message)
	if len(message) > 1000 {
		message = message[:1000] + "…"
	}
	return fmt.Sprintf("Context Drop task %s: %s\n%s", shortRunID(report.RunID), kind, message)
}

func shortRunID(id string) string {
	if len(id) > 18 {
		return id[len(id)-18:]
	}
	return id
}
