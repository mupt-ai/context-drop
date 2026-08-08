package imessage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var benchmarkPrompt string

func BenchmarkPromptBuildWarmPersistent(b *testing.B) {
	cfg := Defaults()
	cfg.Trusted = true
	cfg.PersonaFile = "/private/context/SOUL.md"
	cfg.MemoryFile = "/private/context/MEMORY.md"
	cfg.ConversationArchiveFile = "/private/context/chat_full.jsonl"
	adapter := Adapter{Config: cfg}
	message := Message{ID: "benchmark", Text: "what is the current status?"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prompt, err := adapter.buildPrompt(message, false)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkPrompt = prompt
	}
	b.ReportMetric(float64(len(benchmarkPrompt)), "prompt-bytes")
}

func BenchmarkPromptBuildBootstrapArchive(b *testing.B) {
	dir := b.TempDir()
	persona := filepath.Join(dir, "SOUL.md")
	memory := filepath.Join(dir, "MEMORY.md")
	archive := filepath.Join(dir, "chat_full.jsonl")
	if err := os.WriteFile(persona, []byte(strings.Repeat("persona context\n", 250)), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(memory, []byte(strings.Repeat("durable memory\n", 1100)), 0o600); err != nil {
		b.Fatal(err)
	}
	var corpus bytes.Buffer
	encoder := json.NewEncoder(&corpus)
	for i := 0; corpus.Len() < 3*1024*1024; i++ {
		text := strings.Repeat("representative conversation history and orchestration details ", 12)
		if i%100 == 0 {
			text += " latency current status persistent session"
		}
		if err := encoder.Encode(chatLine{TS: "2026-08-08T00:00:00Z", Role: "assistant", Text: text}); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(archive, corpus.Bytes(), 0o600); err != nil {
		b.Fatal(err)
	}
	cfg := Defaults()
	cfg.Trusted = true
	cfg.PersonaFile = persona
	cfg.MemoryFile = memory
	cfg.ConversationArchiveFile = archive
	adapter := Adapter{Config: cfg}
	message := Message{ID: "benchmark", Text: "what is the current latency status?"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prompt, err := adapter.buildPrompt(message, true)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkPrompt = prompt
	}
	b.ReportMetric(float64(len(benchmarkPrompt)), "prompt-bytes")
}
