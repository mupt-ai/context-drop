package imessage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationExcerptsHeadPlusRelevant(t *testing.T) {
	dir := t.TempDir()
	corpus := filepath.Join(dir, "chat_full.jsonl")
	var lines []string
	write := func(ts, role, text string) {
		b, _ := json.Marshal(chatLine{TS: ts, Role: role, Text: text})
		lines = append(lines, string(b))
	}
	write("2026-04-06T22:03:38Z", "user", "Hey")
	write("2026-04-06T22:03:40Z", "assistant", "yo what's up")
	// Filler pushes the relevant exchange past the head budget.
	for i := 0; i < 10; i++ {
		write("2026-04-06T22:04:00Z", "assistant", strings.Repeat("a general filler response", 2000))
	}
	write("2026-04-06T22:05:00Z", "user", "my favorite golf course is Shoreline, I play most weekends")
	write("2026-04-06T22:05:05Z", "assistant", "noted, Shoreline it is")
	write("2026-04-06T22:06:00Z", "user", "what did I say about langchain partnership")
	if err := os.WriteFile(corpus, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := conversationExcerpts(corpus, "what golf course do I play")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[2026-04-06T22:03:38Z] Avyay: Hey", "Shoreline"} {
		if !strings.Contains(got, want) {
			t.Fatalf("excerpts missing %q:\n%s", want, got)
		}
	}
}
