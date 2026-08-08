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

type queueCommander struct {
	mu           sync.Mutex
	history      []map[string]any
	responds     int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (f *queueCommander) Run(_ context.Context, _ string, args []string, _ int) (imessage.CommandResult, error) {
	if len(args) > 0 && args[0] == "history" {
		f.mu.Lock()
		data, _ := json.Marshal(f.history)
		f.mu.Unlock()
		return imessage.CommandResult{Stdout: data}, nil
	}
	if len(args) > 0 && args[0] == "send" {
		return imessage.CommandResult{Stdout: []byte(`{"ok":true}`)}, nil
	}
	f.mu.Lock()
	f.responds++
	count := f.responds
	f.mu.Unlock()
	if count == 1 {
		close(f.firstStarted)
		<-f.releaseFirst
	}
	return imessage.CommandResult{Stdout: []byte("reply")}, nil
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
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		return nil
	}); err != nil {
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

func TestMessageJobRecordsEndToEndStageTelemetry(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	clock := created.Add(2 * time.Second)
	now := func() time.Time {
		current := clock
		clock = clock.Add(10 * time.Millisecond)
		return current
	}
	commander := &messageCommander{history: []map[string]any{{
		"id": "measured", "text": "hello", "chat_id": "1", "is_from_me": false, "created_at": created.Format(time.RFC3339Nano),
	}}}
	runner := &Runner{Store: store, Now: now, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	runner.PollMessages(context.Background())
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	job := state.MessageJobs["measured"]
	if job.Status != "sent" || job.Latency.MessageCreatedAt == nil {
		t.Fatalf("job = %#v", job)
	}
	if job.Latency.QueueMS != 2010 || job.Latency.WorkerQueueMS != 10 || job.Latency.ServiceMS != 20 || job.Latency.EndToEndMS != 2030 {
		t.Fatalf("latency = %#v", job.Latency)
	}
	if job.Latency.PromptBytes == 0 {
		t.Fatalf("prompt bytes were not recorded: %#v", job.Latency)
	}
}

func TestPollingClaimsNextMessageWhileSessionWorkerIsBusy(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commander := &queueCommander{
		history:      []map[string]any{{"id": "first", "text": "one", "chat_id": "1", "is_from_me": false}},
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	runner := &Runner{Store: store, Now: func() time.Time { return time.Now().UTC() }, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	firstDone := make(chan struct{})
	go func() {
		runner.PollMessages(context.Background())
		close(firstDone)
	}()
	select {
	case <-commander.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first responder did not start")
	}

	commander.mu.Lock()
	commander.history = append(commander.history, map[string]any{"id": "second", "text": "two", "chat_id": "1", "is_from_me": false})
	commander.mu.Unlock()
	secondDone := make(chan struct{})
	go func() {
		runner.PollMessages(context.Background())
		close(secondDone)
	}()

	deadline := time.After(time.Second)
	for {
		state, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if state.MessageJobs["second"].Status == "queued" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second message was not claimed while first responder was busy")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(commander.releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first poll did not finish")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second poll did not finish")
	}
	state, _ := store.Load()
	if state.MessageJobs["first"].Status != "sent" || state.MessageJobs["second"].Status != "sent" {
		t.Fatalf("jobs = %#v", state.MessageJobs)
	}
}
