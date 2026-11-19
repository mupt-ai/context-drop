package mods

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestCronMatchesFiveFieldSchedules(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC) // a Monday
	for spec, want := range map[string]bool{
		"30 9 * * *":      true,
		"*/15 * * * *":    true,
		"*/7 * * * *":     false,
		"0-30/10 9 * * 1": true,
		"30 9 * * 2-5":    false,
		"30 9,17 16 11 *": true,
	} {
		c, err := parseCron(spec)
		if err != nil || c.matches(at) != want {
			t.Errorf("%q at %s = %v, %v; want %v", spec, at, c.matches(at), err, want)
		}
	}
	for _, bad := range []string{"* * * *", "61 * * * *", "*/0 * * * *", "5-1 * * * *"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAgentEventsDescribeWhatChanged(t *testing.T) {
	before := []map[string]any{{"id": "m/a", "status": "idle"}, {"id": "m/b", "status": "working"}}
	after := []map[string]any{{"id": "m/a", "status": "blocked"}, {"id": "m/c", "status": "working"}}
	evs := AgentEvents(before, after)
	got := []string{}
	for _, e := range evs {
		got = append(got, e["type"].(string)+" "+e["agent"].(map[string]any)["id"].(string))
	}
	want := []string{"agent.status m/a", "agent.started m/c", "agent.ended m/b"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("events = %v", got)
	}
	if evs[0]["from"] != "idle" || evs[0]["to"] != "blocked" {
		t.Fatalf("status event = %v", evs[0])
	}
}

func TestSlackSignaturesExpire(t *testing.T) {
	sign := func(ts string) http.Header {
		h := http.Header{}
		h.Set("X-Slack-Request-Timestamp", ts)
		mac := hmacHex("k", "v0:"+ts+":body")
		h.Set("X-Slack-Signature", "v0="+mac)
		return h
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if !verify("slack", "k", sign(now), []byte("body")) {
		t.Fatal("a fresh slack signature was refused")
	}
	old := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if verify("slack", "k", sign(old), []byte("body")) {
		t.Fatal("a ten-minute-old slack signature was accepted")
	}
}
