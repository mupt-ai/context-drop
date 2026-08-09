package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/runtimeclient"
)

const imessageRouterID = "imessage-router"

func (r *Runner) configureRouter(ctx context.Context) error {
	if r.Delegation == nil {
		return fmt.Errorf("delegation runtime is unavailable")
	}
	if err := r.Delegation.Health(ctx); err != nil {
		return err
	}
	capability, err := r.Delegation.IssueRouterCapability(ctx, imessageRouterID, r.IMessage.Config.ChatID)
	if err != nil {
		return err
	}
	responder, ok := r.IMessage.PersistentResponder.(*imessage.PiRPCResponder)
	if !ok {
		return fmt.Errorf("router mode requires the persistent Pi RPC responder")
	}
	client, ok := r.Delegation.(*runtimeclient.Client)
	if !ok {
		return fmt.Errorf("delegation runtime does not expose a loopback address")
	}
	responder.SetDelegationEnv(strings.TrimRight(client.Address, "/")+"/v1/delegate", capability)
	return nil
}

func (r *Runner) configureRouterWithRetry(ctx context.Context, attempts int, delay time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, time.Second)
		last = r.configureRouter(attemptCtx)
		cancel()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return last
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
	if r.IMessage == nil || !r.IMessage.Config.Enabled || r.Delegation == nil {
		return
	}
	for {
		report, leased, err := r.Delegation.LeaseReport(ctx, imessageRouterID, r.IMessage.Config.ChatID)
		if err != nil || !leased {
			return
		}
		message := formatReport(report)
		sendCtx, cancel := context.WithTimeout(ctx, time.Duration(r.IMessage.Config.SendTimeoutSeconds)*time.Second)
		sendErr := r.IMessage.Send(sendCtx, message)
		cancel()
		_ = r.Delegation.FinishReport(ctx, report, imessageRouterID, r.IMessage.Config.ChatID, sendErr == nil)
		if sendErr != nil {
			return
		}
	}
}

func (r *Runner) confirmSensitiveAction(ctx context.Context, chatID, incoming string) (string, bool) {
	const prefix = "CONFIRM "
	if chatID != r.IMessage.Config.ChatID || !strings.HasPrefix(incoming, prefix) || strings.TrimSpace(incoming) != incoming {
		return "", false
	}
	token := strings.TrimPrefix(incoming, prefix)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	run, err := r.Delegation.Confirm(ctx, imessageRouterID, r.IMessage.Config.ChatID, token)
	if err != nil {
		return "that confirmation token is invalid, expired, already used, or belongs to another chat.", true
	}
	return fmt.Sprintf("confirmed — i started authorized worker %s. the authorization is limited to the challenged action.", shortRunID(run.ID)), true
}

func flattenReportText(value string) string {
	var b strings.Builder
	space := false
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	text := strings.TrimSpace(b.String())
	for utf8.RuneCountInString(text) > 1000 {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

func formatReport(report runtimeclient.ParentReport) string {
	kind := map[string]string{"started": "started", "progress": "progress", "needs_user": "needs your input", "completed": "done", "failed": "failed"}[report.Kind]
	if kind == "" {
		kind = "update"
	}
	message := flattenReportText(report.Message)
	result := fmt.Sprintf("Worker report (not independently verified) — task %s: %s\n%s", shortRunID(report.RunID), kind, message)
	if report.SensitiveAction != "" && report.ChallengeToken != "" {
		result += fmt.Sprintf("\nSensitive action blocked. To authorize only this challenged action, reply exactly: CONFIRM %s", report.ChallengeToken)
	}
	return result
}

func shortRunID(id string) string {
	if len(id) > 18 {
		return id[len(id)-18:]
	}
	return id
}
