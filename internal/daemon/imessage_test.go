package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
)

type messageCommander struct {
	mu       sync.Mutex
	history  []map[string]any
	responds int
	sends    []string
}

func (f *messageCommander) Run(_ context.Context, name string, args []string, _ int) (imessage.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) > 0 && args[0] == "history" {
		data, _ := json.Marshal(f.history)
		return imessage.CommandResult{Stdout: data}, nil
	}
	if len(args) > 0 && args[0] == "send" {
		f.sends = append(f.sends, args[4])
		return imessage.CommandResult{Stdout: []byte(`{"ok":true}`)}, nil
	}
	f.responds++
	return imessage.CommandResult{Stdout: []byte("reply")}, nil
}

func messageTestConfig(t *testing.T) imessage.Config {
	t.Helper()
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	executable := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.ChatID = "1"
	cfg.ImsgPath = executable
	cfg.ResponderCommand = []string{executable, "{prompt_file}"}
	return cfg
}

func TestMessageInitialSyncThenReplyOnceAcrossRestart(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	commander := &messageCommander{history: []map[string]any{{"id": "old", "text": "old", "chat_id": "1", "is_from_me": false}}}
	runner := &Runner{Store: store, Now: func() time.Time { return now }, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	runner.PollMessages(context.Background())
	if commander.responds != 0 || len(commander.sends) != 0 {
		t.Fatal("initial sync replied")
	}
	state, _ := store.Load()
	if !state.IMessageInitialized || state.SeenMessageIDs["old"] == "" {
		t.Fatalf("state = %#v", state)
	}

	commander.history = append(commander.history, map[string]any{"id": "new", "text": "hello", "chat_id": "1", "is_from_me": false})
	now = now.Add(time.Second)
	runner.PollMessages(context.Background())
	if commander.responds != 1 || len(commander.sends) != 1 || commander.sends[0] != "reply" {
		t.Fatalf("responds=%d sends=%#v", commander.responds, commander.sends)
	}

	// A fresh Runner models daemon restart: durable seen state prevents duplicates.
	restarted := &Runner{Store: store, Now: func() time.Time { return now.Add(time.Second) }, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	restarted.PollMessages(context.Background())
	if commander.responds != 1 || len(commander.sends) != 1 {
		t.Fatal("restart duplicated reply")
	}
	state, _ = store.Load()
	if state.MessageJobs["new"].Status != "sent" || state.MessageJobs["new"].SentAt == nil {
		t.Fatalf("job = %#v", state.MessageJobs["new"])
	}
}

func TestOutgoingAndOtherChatNeverReachResponder(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error { st.IMessageInitialized = true; return nil }); err != nil {
		t.Fatal(err)
	}
	commander := &messageCommander{history: []map[string]any{
		{"id": "self", "text": "self", "chat_id": "1", "is_from_me": true},
		{"id": "other", "text": "other", "chat_id": "2", "is_from_me": false},
	}}
	runner := &Runner{Store: store, Now: func() time.Time { return time.Now().UTC() }, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	runner.PollMessages(context.Background())
	if commander.responds != 0 || len(commander.sends) != 0 {
		t.Fatal("filtered message reached responder")
	}
}
