package pool

import "testing"

func TestReplyIsWhatFollowsTheDeliveredMessage(t *testing.T) {
	entries := []Entry{
		{Type: "user", Text: "run   the\nplan"},
		{Type: "assistant", Text: "old answer"},
		{Type: "user", Text: "[context-drop] From laptop.\n\nrun the plan"},
		{Type: "tool", Name: "Bash"},
	}
	if _, ok := Reply(entries, "run the plan"); ok {
		t.Fatal("answered before the agent wrote anything after the latest delivery")
	}
	entries = append(entries, Entry{Type: "assistant", Text: "done"})
	got, ok := Reply(entries, "run the plan")
	if !ok || len(got) != 3 || got[2].Text != "done" {
		t.Fatalf("reply = %+v, %v", got, ok)
	}
}
