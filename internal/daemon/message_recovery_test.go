package daemon

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
)

func TestMessageBurstIsOneDurableTurn(t *testing.T) {
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	now := time.Now().UTC()
	responder := &recordingResponder{response: imessage.Response{ToolCompleted: true}}
	commander := &messageCommander{}
	runner := &Runner{Store: store, Now: time.Now, messageDebounce: 20 * time.Millisecond, IMessage: &imessage.Adapter{Config: messageTestConfig(t), Commander: commander, PersistentResponder: responder}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var done <-chan struct{}
	for _, text := range []string{"A", "B", "C"} {
		if err := store.Update(func(st *orchestrator.State) error {
			st.MessageJobs[text] = orchestrator.MessageJob{MessageID: text, Status: "queued", ClaimedAt: now, Input: &orchestrator.MessageInput{Text: text, ChatID: "chat"}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var err error
		done, err = runner.enqueueMessages(ctx, []imessage.Message{{ID: text, Text: text, ChatID: "chat"}}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("burst did not settle")
	}
	if !reflect.DeepEqual(responder.prompts, []string{"A\n\nB\n\nC"}) || len(commander.sends) != 0 {
		t.Fatalf("prompts=%v sends=%v", responder.prompts, commander.sends)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "C"} {
		if state.MessageJobs[id].Status != "handled" || state.MessageJobs[id].Input != nil {
			t.Fatalf("job=%+v", state.MessageJobs[id])
		}
	}
	if len(state.RecentOutbound) != 0 {
		t.Fatal("silent turn created an outbound message")
	}
}

func TestProcessingDoesNotExecuteWithoutDurableState(t *testing.T) {
	commander := &messageCommander{}
	runner := &Runner{Store: orchestrator.Store{Path: t.TempDir()}, Now: time.Now, IMessage: &imessage.Adapter{Config: messageTestConfig(t), Commander: commander}}
	runner.processMessage(context.Background(), imessage.Message{ID: "1", Text: "hello", ChatID: "1"})
	if commander.responds != 0 || len(commander.sends) != 0 {
		t.Fatal("executed a turn despite failed state transition")
	}
}

func TestRecoverMessagesReplaysOnlyDurablyQueuedInput(t *testing.T) {
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	now := time.Now().UTC()
	err := store.Update(func(st *orchestrator.State) error {
		st.IMessageChatID = "1"
		st.IMessageInitialized = true
		st.MessageJobs["queued"] = orchestrator.MessageJob{MessageID: "queued", Status: "queued", ClaimedAt: now, Input: &orchestrator.MessageInput{Text: "hello", ChatID: "1"}}
		st.MessageJobs["interrupted"] = orchestrator.MessageJob{MessageID: "interrupted", Status: "processing", ClaimedAt: now, Input: &orchestrator.MessageInput{Text: "do not repeat", ChatID: "1"}}
		st.MessageJobs["legacy"] = orchestrator.MessageJob{MessageID: "legacy", Status: "queued", ClaimedAt: now}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &Runner{Store: store, Now: func() time.Time { return now }, IMessage: &imessage.Adapter{Config: messageTestConfig(t), Commander: &messageCommander{}}}
	if err := runner.recoverMessages(ctx); err != nil {
		t.Fatal(err)
	}
	waitForMessageJobs(t, store, "queued")
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"interrupted", "legacy"} {
		if st.MessageJobs[id].Status != "unknown" {
			t.Fatalf("%s status=%s", id, st.MessageJobs[id].Status)
		}
	}
	if st.MessageJobs["queued"].Input != nil {
		t.Fatal("completed input retained")
	}
}

func TestImageMessagesReachTheResponderWithAttachments(t *testing.T) {
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	now := time.Now().UTC()
	responder := &recordingResponder{response: imessage.Response{ToolCompleted: true}}
	runner := &Runner{Store: store, Now: time.Now, messageDebounce: 20 * time.Millisecond, IMessage: &imessage.Adapter{Config: messageTestConfig(t), Commander: &messageCommander{}, PersistentResponder: responder}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	photo := imessage.Attachment{Path: "/tmp/photo.jpg", MimeType: "image/jpeg", Name: "photo.jpg"}
	messages := []imessage.Message{
		{ID: "1", Text: "\uFFFC", ChatID: "chat", Attachments: []imessage.Attachment{photo}},
		{ID: "2", Text: "what is this?", ChatID: "chat"},
	}
	var done <-chan struct{}
	for _, message := range messages {
		message := message
		if err := store.Update(func(st *orchestrator.State) error {
			st.MessageJobs[message.ID] = orchestrator.MessageJob{MessageID: message.ID, Status: "queued", ClaimedAt: now, Input: &orchestrator.MessageInput{Text: message.Text, ChatID: "chat", Attachments: storedAttachments(message.Attachments)}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var err error
		done, err = runner.enqueueMessages(ctx, []imessage.Message{message}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("burst did not settle")
	}
	want := []string{"Image attachment (also provided inline): /tmp/photo.jpg (image/jpeg)\n\nwhat is this?"}
	if !reflect.DeepEqual(responder.prompts, want) {
		t.Fatalf("prompts = %q", responder.prompts)
	}
	if !reflect.DeepEqual(responder.attachments, [][]imessage.Attachment{{photo}}) {
		t.Fatalf("attachments = %#v", responder.attachments)
	}
}

func TestRequeuedMessageJobsKeepAttachments(t *testing.T) {
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	photo := orchestrator.MessageAttachment{Path: "/tmp/photo.png", MimeType: "image/png"}
	if err := store.Update(func(st *orchestrator.State) error {
		st.MessageJobs["9"] = orchestrator.MessageJob{MessageID: "9", Status: "queued", ClaimedAt: time.Now(), Input: &orchestrator.MessageInput{Text: "\uFFFC", ChatID: "chat", Attachments: []orchestrator.MessageAttachment{photo}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := messageAttachments(state.MessageJobs["9"].Input.Attachments)
	if !reflect.DeepEqual(got, []imessage.Attachment{{Path: "/tmp/photo.png", MimeType: "image/png"}}) {
		t.Fatalf("attachments = %#v", got)
	}
}
