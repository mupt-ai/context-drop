package pool

import (
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
)

func TestFromSessionNamesAgentsAcrossTheOrg(t *testing.T) {
	sub := FromSession("laptop", agents.Session{ID: "cc-00000002", Agent: "claude", Subagent: true, ParentID: "cc-00000001",
		Name: "scan repo", Title: "Explore", Status: "idle", App: agents.AppClaudeDesktop})
	if sub.ID != "laptop/cc-00000002" || sub.Role != RoleSubagent || sub.Root != "laptop/cc-00000001" ||
		sub.AgentType != "Explore" || sub.Location == nil || sub.Location.App != "claude" {
		t.Fatalf("subagent = %+v", sub)
	}
	main := FromSession("laptop", agents.Session{ID: "cc-00000001", Agent: "claude", Status: "working",
		Location: &agents.Location{Mux: "tmux", Session: "main", Window: "2", Pane: "%12"}})
	if main.Role != RoleMain || main.Location.Mux != "tmux" || main.Location.Pane.ID != "%12" || main.LocationLabel != "tmux main:2 %12" {
		t.Fatalf("main = %+v", main)
	}
}

func TestNarrowStartsAtTheLastCompaction(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 17, 10, m, 0, 0, time.UTC) }
	entries := FromMessages([]agents.Message{
		{Role: "user", Text: "fix the tests", Time: at(1)},
		{Role: "system", Text: "Conversation compacted (manual); the messages before this were summarized.", Time: at(2)},
		{Role: "assistant", Text: "on it", Time: at(3)},
		{Role: "error", Text: "API Error: 529", Time: at(4)},
	})
	res := Narrow(Agent{ID: "laptop/cc-00000001"}, entries, ReadOptions{})
	if res.Runs != 2 || res.Run != 2 || len(res.Entries) != 3 || res.Entries[0].Trigger != "manual" || res.Entries[2].Type != "error" {
		t.Fatalf("current run = %+v", res)
	}
	if all := Narrow(Agent{}, entries, ReadOptions{All: true, Last: 2}); all.Run != 1 || len(all.Entries) != 2 {
		t.Fatalf("all, last 2 = %+v", all)
	}
	if newer := Narrow(Agent{}, entries, ReadOptions{After: res.Entries[1].Time}); len(newer.Entries) != 1 || newer.Entries[0].Type != "error" {
		t.Fatalf("after cursor = %+v", newer)
	}
}
