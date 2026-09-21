package daemon

import (
	"context"
	"log"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
)

// The orchestrator's live context is rebuilt by pi from its session file on
// every turn, and the router hops between backends, so prompt caching rarely
// helps: each turn pays a cold prefill of the whole history. Compaction near
// the context window (pi's auto-compaction) leaves turns slow for months at a
// time. Instead, once the context outgrows a small budget, compact it back
// down in the background, between turns, where the latency is invisible.
const (
	// compactThresholdTokens is the live-context size that triggers a
	// background compaction after the current turn finishes.
	compactThresholdTokens int64 = 30_000
	// compactLargeContextTokens marks a context so large that its one-time
	// descent should wait for a real idle window instead of the short gap
	// between consecutive texts.
	compactLargeContextTokens int64 = 50_000
	// compactQuickIdle is how long to let the conversation stay quiet after a
	// normal-sized turn before compacting.
	compactQuickIdle = 2 * time.Second
	// compactDeepIdle is the idle wait before compacting a very large context;
	// its summarize call is slow enough to matter.
	compactDeepIdle = 60 * time.Second
	// compactTimeout bounds the compaction RPC itself.
	compactTimeout = 10 * time.Minute
)

const compactInstructions = `Keep the details the main orchestrator needs to keep working as Avyay's assistant: current goals and open questions, in-flight tasks and their state, key decisions with their rationale, and standing preferences from AGENTS.md style. Health, workout, and food state should survive: recent logged meals, lifts, body weight, habits, and any targets. Drop stale internal traffic: completed one-off worker reports, schedule-failure notices already acted on, and superseded research. Do not lose anything Avyay asked for that has not been delivered yet.`

// maybeCompactOrchestrator schedules a background compaction after a completed
// orchestrator turn whose final prefill exceeded the token budget. It never
// blocks or delays the caller; the reply has already been sent by the time this
// runs.
func maybeCompactOrchestrator(adapter *imessage.Adapter, response imessage.Response) {
	inputTokens := lastRoundInputTokens(response)
	if inputTokens < compactThresholdTokens || adapter == nil {
		return
	}
	idle := compactQuickIdle
	if inputTokens >= compactLargeContextTokens {
		idle = compactDeepIdle
	}
	log.Printf("Context Drop orchestrator context at %d tokens; compacting in the background in %s", inputTokens, idle)
	go func() {
		time.Sleep(idle)
		ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
		defer cancel()
		result, attempted, err := adapter.CompactOrchestratorIfIdle(ctx, compactInstructions)
		if err != nil {
			log.Printf("Context Drop orchestrator compaction failed: %v", err)
			return
		}
		if !attempted {
			log.Printf("Context Drop orchestrator compaction skipped: a turn resumed before the orchestrator went idle")
			return
		}
		log.Printf("Context Drop orchestrator context compacted: %d -> %d tokens", result.TokensBefore, result.EstimatedTokensAfter)
	}()
}

// lastRoundInputTokens returns the final model round's prompt size, which
// includes everything the session rebuilt for that turn.
func lastRoundInputTokens(response imessage.Response) int64 {
	var input int64
	for _, round := range response.Metrics.ModelRounds {
		if round.InputTokens > input {
			input = round.InputTokens
		}
	}
	return input
}
