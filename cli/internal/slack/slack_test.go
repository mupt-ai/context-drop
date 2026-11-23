package slack

import (
	"strings"
	"testing"
	"time"
)

func TestVerifyAcceptsOnlyFreshSignedBodies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"type":"event_callback"}`)
	h := Sign("shh", body, now)
	if err := Verify("shh", h, body, now.Add(time.Minute)); err != nil {
		t.Fatalf("a fresh signed body: %v", err)
	}
	if err := Verify("shh", h, []byte(`{"type":"other"}`), now); err == nil {
		t.Fatal("a changed body passed")
	}
	if err := Verify("other", h, body, now); err == nil {
		t.Fatal("another app's secret passed")
	}
	if err := Verify("shh", h, body, now.Add(MaxSkew+time.Second)); err == nil {
		t.Fatal("a replayed old request passed")
	}
}

func TestManifestPointsSlackAtTheServer(t *testing.T) {
	m := Manifest("Context Drop", "https://ctx.example.com/")
	for _, want := range []string{"https://ctx.example.com/v1/slack/events", "https://ctx.example.com/v1/slack/oauth", "message.im", "chat:write"} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest lacks %q", want)
		}
	}
}
