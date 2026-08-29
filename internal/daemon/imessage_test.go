package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type restartingMessageWatcher struct {
	mu    sync.Mutex
	calls []int64
}

func (w *restartingMessageWatcher) Watch(ctx context.Context, cursor int64, handle func(imessage.Message) error) error {
	w.mu.Lock()
	run := len(w.calls)
	w.calls = append(w.calls, cursor)
	w.mu.Unlock()
	switch run {
	case 0:
		for _, message := range []imessage.Message{
			{ID: "101", ChatID: "1", Text: "outgoing", FromMe: true},
			{ID: "102", ChatID: "1", Text: "first", CreatedAt: time.Now().Add(-100 * time.Millisecond).UTC().Format(time.RFC3339Nano)},
		} {
			if err := handle(message); err != nil {
				return err
			}
		}
		return errors.New("transient watch failure")
	case 1:
		for _, message := range []imessage.Message{
			{ID: "102", ChatID: "1", Text: "duplicate"},
			{ID: "103", ChatID: "1", Text: "second", CreatedAt: time.Now().Add(-100 * time.Millisecond).UTC().Format(time.RFC3339Nano)},
		} {
			if err := handle(message); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return ctx.Err()
	default:
		return errors.New("unexpected third watch run")
	}
}

type unsupportedMessageWatcher struct{}

func (unsupportedMessageWatcher) Watch(context.Context, int64, func(imessage.Message) error) error {
	return imessage.ErrWatchUnsupported
}

type silentFailureMessageWatcher struct {
	mu    sync.Mutex
	calls int
}

func (w *silentFailureMessageWatcher) Watch(context.Context, int64, func(imessage.Message) error) error {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	return errors.New("imsg watch exited unexpectedly")
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

func messageTestConfig(t testing.TB) imessage.Config {
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

func BenchmarkPollMessagesSeenHistory(b *testing.B) {
	cfg := messageTestConfig(b)
	store := orchestrator.Store{Path: filepath.Join(b.TempDir(), "state.json")}
	history := make([]map[string]any, 20)
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		for i := range history {
			id := fmt.Sprintf("seen-%d", i)
			history[i] = map[string]any{"id": id, "text": "old", "chat_id": "1", "is_from_me": false}
			st.SeenMessageIDs[id] = "2026-08-08T00:00:00Z"
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	commander := &messageCommander{history: history}
	runner := &Runner{Store: store, Now: func() time.Time { return time.Now().UTC() }, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	b.ResetTimer()
	for b.Loop() {
		runner.PollMessages(context.Background())
	}
}

func waitForMessageSends(t *testing.T, commander *messageCommander, count int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		commander.mu.Lock()
		sends := len(commander.sends)
		commander.mu.Unlock()
		if sends >= count {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d sends; got %d", count, sends)
		case <-ticker.C:
		}
	}
}

func waitForMessageJobs(t *testing.T, store orchestrator.Store, ids ...string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		complete := true
		for _, id := range ids {
			complete = complete && state.MessageJobs[id].Status == "sent"
		}
		if complete {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for sent jobs %v", ids)
		case <-ticker.C:
		}
	}
}

func TestMessageWatchRestartsFromDurableCursorAndTracksOutgoingWithoutReplying(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		st.IMessageCursor = 100
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commander := &messageCommander{}
	watcher := &restartingMessageWatcher{}
	runner := &Runner{
		Store:                store,
		Now:                  func() time.Time { return time.Now().UTC() },
		IMessage:             &imessage.Adapter{Config: cfg, Commander: commander, Watcher: watcher},
		MessageWatchRetryMin: time.Millisecond,
		MessageWatchRetryMax: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.WatchMessages(ctx) }()
	waitForMessageSends(t, commander, 2)
	waitForMessageJobs(t, store, "102", "103")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("WatchMessages() error = %v", err)
	}

	watcher.mu.Lock()
	calls := append([]int64(nil), watcher.calls...)
	watcher.mu.Unlock()
	if len(calls) != 2 || calls[0] != 100 || calls[1] != 102 {
		t.Fatalf("watch cursors = %#v", calls)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.IMessageCursor != 103 {
		t.Fatalf("cursor = %d", state.IMessageCursor)
	}
	foundOutgoing := false
	for _, outbound := range state.RecentOutbound {
		foundOutgoing = foundOutgoing || (outbound.MessageID == "101" && outbound.Text == "outgoing")
	}
	if state.SeenMessageIDs["101"] == "" || state.MessageJobs["101"].MessageID != "" || !foundOutgoing {
		t.Fatalf("outgoing tracking is wrong: seen=%q job=%#v outbound=%#v", state.SeenMessageIDs["101"], state.MessageJobs["101"], state.RecentOutbound)
	}
	for _, id := range []string{"102", "103"} {
		if state.MessageJobs[id].Status != "sent" {
			t.Fatalf("job %s = %#v", id, state.MessageJobs[id])
		}
	}
	commander.mu.Lock()
	responds := commander.responds
	sends := len(commander.sends)
	commander.mu.Unlock()
	if responds != 2 || sends != 2 {
		t.Fatalf("responds=%d sends=%d", responds, sends)
	}
}

func TestRecentOutboundSyncRecoversMessagesAlreadyPastCursor(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	commander := &messageCommander{history: []map[string]any{{"id": "101", "text": "What did you eat?", "chat_id": "1", "is_from_me": true, "created_at": "2026-08-29T05:00:00Z"}}}
	runner := &Runner{Store: store, Now: func() time.Time { return time.Now().UTC() }, IMessage: &imessage.Adapter{Config: cfg, Commander: commander}}
	if err := runner.syncRecentOutbound(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.RecentOutbound) != 1 || state.RecentOutbound[0].MessageID != "101" || state.RecentOutbound[0].Text != "What did you eat?" {
		t.Fatalf("outbound=%#v", state.RecentOutbound)
	}
}

func TestMessageWatchMigratesCursorFromSeenRowIDs(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		st.SeenMessageIDs["9389"] = "2026-08-08T00:00:00Z"
		st.SeenMessageIDs["9391"] = "2026-08-08T00:01:00Z"
		st.SeenMessageIDs["legacy-hash"] = "2026-08-08T00:02:00Z"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Store: store, Now: func() time.Time { return time.Now().UTC() }, IMessage: &imessage.Adapter{Config: cfg}}
	cursor, err := runner.prepareMessageWatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 9391 {
		t.Fatalf("cursor = %d", cursor)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.IMessageCursor != 9391 {
		t.Fatalf("persisted cursor = %d", state.IMessageCursor)
	}
}

func TestSilentMessageWatchFailureFallsBackToPollingAfterBoundedRetries(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commander := &messageCommander{history: []map[string]any{{"id": "202", "text": "silent fallback", "chat_id": "1", "is_from_me": false}}}
	watcher := &silentFailureMessageWatcher{}
	runner := &Runner{
		Store:                    store,
		Now:                      func() time.Time { return time.Now().UTC() },
		IMessage:                 &imessage.Adapter{Config: cfg, Commander: commander, Watcher: watcher},
		MessagePollInterval:      10 * time.Millisecond,
		MessageWatchRetryMin:     time.Millisecond,
		MessageWatchRetryMax:     time.Millisecond,
		MessageWatchFailureLimit: 2,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.ReceiveMessages(ctx)
		close(done)
	}()
	waitForMessageSends(t, commander, 1)
	waitForMessageJobs(t, store, "202")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ReceiveMessages did not stop")
	}
	watcher.mu.Lock()
	calls := watcher.calls
	watcher.mu.Unlock()
	if calls != 2 {
		t.Fatalf("watch calls = %d, want 2", calls)
	}
}

func TestUnsupportedMessageWatchFallsBackToPolling(t *testing.T) {
	cfg := messageTestConfig(t)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := store.Update(func(st *orchestrator.State) error {
		st.IMessageInitialized = true
		st.IMessageChatID = "1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commander := &messageCommander{history: []map[string]any{{"id": "201", "text": "fallback", "chat_id": "1", "is_from_me": false}}}
	runner := &Runner{
		Store:               store,
		Now:                 func() time.Time { return time.Now().UTC() },
		IMessage:            &imessage.Adapter{Config: cfg, Commander: commander, Watcher: unsupportedMessageWatcher{}},
		MessagePollInterval: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.ReceiveMessages(ctx)
		close(done)
	}()
	waitForMessageSends(t, commander, 1)
	waitForMessageJobs(t, store, "201")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ReceiveMessages did not stop")
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.MessageJobs["201"].Status != "sent" || state.IMessageCursor != 201 {
		t.Fatalf("fallback state = %#v", state)
	}
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
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.RecentOutbound) != 1 || state.RecentOutbound[0].Text != "self" || state.SeenMessageIDs["other"] != "" {
		t.Fatalf("outbound=%#v seen-other=%q", state.RecentOutbound, state.SeenMessageIDs["other"])
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
