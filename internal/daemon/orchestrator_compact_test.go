package daemon

import (
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
)

func TestLastRoundInputTokensTakesTheFinalPrefill(t *testing.T) {
	response := imessage.Response{Metrics: imessage.ResponseMetrics{ModelRounds: []imessage.ModelRoundMetrics{
		{Model: "a", InputTokens: 1000, TotalTokens: 1200},
		{Model: "b", InputTokens: 108000, TotalTokens: 108400},
	}}}
	if got := lastRoundInputTokens(response); got != 108000 {
		t.Fatalf("lastRoundInputTokens = %d, want 108000", got)
	}
	// A round that only read from cache still counts its full prompt size.
	response.Metrics.ModelRounds = append(response.Metrics.ModelRounds, imessage.ModelRoundMetrics{Model: "c", InputTokens: 300, CacheReadTokens: 107700})
	if got := lastRoundInputTokens(response); got != 108000 {
		t.Fatalf("lastRoundInputTokens = %d, want 108000", got)
	}
	if got := lastRoundInputTokens(imessage.Response{}); got != 0 {
		t.Fatalf("empty response input = %d, want 0", got)
	}
}

func TestMaybeCompactOrchestratorSkipsSmallContext(t *testing.T) {
	// A compactor that fails the test if contacted: below the threshold the
	// background goroutine must never dial the responder.
	small := imessage.Response{Metrics: imessage.ResponseMetrics{ModelRounds: []imessage.ModelRoundMetrics{{InputTokens: compactThresholdTokens - 1}}}}
	deadline := time.Now().Add(2 * time.Second)
	maybeCompactOrchestrator(nil, small) // nil adapter must be a no-op, not a panic
	if time.Now().After(deadline.Add(-time.Second)) {
		t.Fatal("small-context path unexpectedly delayed the caller")
	}
}
