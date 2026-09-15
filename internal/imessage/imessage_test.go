package imessage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
	max  int
}
type fakeCommander struct {
	calls        []call
	results      []CommandResult
	errors       []error
	promptBodies []string
}

type fakePersistentResponder struct {
	state      PersistentResponderState
	prepareErr error
	respondErr error
	prompt     string
	calls      []string
}

type fakePersistentSender struct {
	chatID string
	text   string
	err    error
}

func (f *fakePersistentSender) Send(_ context.Context, chatID, text string) error {
	f.chatID = chatID
	f.text = text
	return f.err
}

func (f *fakePersistentSender) Close() error { return nil }

func (f *fakePersistentResponder) Prepare(context.Context) (PersistentResponderState, error) {
	f.calls = append(f.calls, "prepare")
	return f.state, f.prepareErr
}

func (f *fakePersistentResponder) Respond(_ context.Context, prompt string, _ int) (Response, error) {
	f.calls = append(f.calls, "respond")
	f.prompt = prompt
	if f.respondErr != nil {
		return Response{}, f.respondErr
	}
	return Response{Reply: "done"}, nil
}

func (f *fakePersistentResponder) Close() error { return nil }

func (f *fakeCommander) Run(_ context.Context, name string, args []string, max int) (CommandResult, error) {
	f.calls = append(f.calls, call{name, append([]string(nil), args...), max})
	if body, err := os.ReadFile(f.calls[len(f.calls)-1].args[len(f.calls[len(f.calls)-1].args)-1]); err == nil {
		f.promptBodies = append(f.promptBodies, string(body))
	}
	index := len(f.calls) - 1
	var result CommandResult
	if index < len(f.results) {
		result = f.results[index]
	}
	var err error
	if index < len(f.errors) {
		err = f.errors[index]
	}
	return result, err
}

func testConfig(t *testing.T) Config {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Enabled = true
	cfg.PersonaFile = filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(cfg.PersonaFile, []byte("orchestrator instructions: use list_tasks, delegate_task, continue_task, herdr_prompt, herdr_overview, herdr_read, herdr_wait, repo_list, and start_agent; never guess identifiers"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.ChatID = "chat;$(nope)"
	cfg.ImsgPath = executable
	cfg.ResponderCommand = []string{executable, "--prompt", "{prompt_file}"}
	return cfg
}

func TestDelegateAllRequiresRouterMode(t *testing.T) {
	cfg := testConfig(t)
	cfg.DelegateAll = true
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "delegate-all mode requires router mode") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRespondToWorkerReportPreparesColdResponderBeforeResponding(t *testing.T) {
	cfg := testConfig(t)
	cfg.RouterMode = true
	responder := &fakePersistentResponder{state: PersistentResponderState{ColdStart: true}}
	adapter := Adapter{Config: cfg, PersistentResponder: responder}

	message, err := adapter.RespondToWorkerReport(context.Background(), "worker status", 100)
	if err != nil {
		t.Fatal(err)
	}
	if message != "done" || !reflect.DeepEqual(responder.calls, []string{"prepare", "respond"}) {
		t.Fatalf("message=%q calls=%v", message, responder.calls)
	}
}

func TestRespondToWorkerReportPreparesWarmResponderBeforeResponding(t *testing.T) {
	cfg := testConfig(t)
	cfg.RouterMode = true
	responder := &fakePersistentResponder{}
	adapter := Adapter{Config: cfg, PersistentResponder: responder}

	if _, err := adapter.RespondToWorkerReport(context.Background(), "worker status", 100); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(responder.calls, []string{"prepare", "respond"}) {
		t.Fatalf("calls=%v", responder.calls)
	}
}

func TestRespondToWorkerReportReturnsPrepareFailureWithoutResponding(t *testing.T) {
	cfg := testConfig(t)
	cfg.RouterMode = true
	responder := &fakePersistentResponder{prepareErr: errors.New("prepare failed")}
	adapter := Adapter{Config: cfg, PersistentResponder: responder}

	_, err := adapter.RespondToWorkerReport(context.Background(), "worker status", 100)
	if err == nil || !strings.Contains(err.Error(), "prepare worker report responder") {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(responder.calls, []string{"prepare"}) {
		t.Fatalf("calls=%v", responder.calls)
	}
}

func TestParseMessagesJSONAndJSONL(t *testing.T) {
	inputs := [][]byte{
		[]byte(`{"messages":[{"id":1,"guid":"a","text":"hello","is_from_me":false,"chat_id":"1","chat_guid":"chat-guid","thread_originator_guid":"root-guid"},{"id":2,"body":"sent","direction":"outgoing"}]}`),
		[]byte("{\"id\":1,\"guid\":\"a\",\"text\":\"hello\",\"chat_id\":\"1\",\"chat_guid\":\"chat-guid\",\"thread_originator_guid\":\"root-guid\"}\n{\"id\":2,\"body\":\"sent\",\"direction\":\"outgoing\"}\n"),
	}
	for _, input := range inputs {
		messages, err := ParseMessages(input)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 2 || messages[0].ID != "1" || messages[0].GUID != "a" || messages[0].ThreadRootGUID != "root-guid" || messages[0].ChatGUID != "chat-guid" || messages[0].Text != "hello" || messages[0].ChatID != "1" || messages[0].FromMe || !messages[1].FromMe {
			t.Fatalf("messages = %#v", messages)
		}
	}
}

func TestHistoryUsesScopedArgvAndFilters(t *testing.T) {
	cfg := testConfig(t)
	payload, _ := json.Marshal([]map[string]any{
		{"id": "incoming", "text": "hello", "chat_id": cfg.ChatID, "is_from_me": false, "created_at": "2024-01-01"},
		{"id": "self", "text": "ignore", "chat_id": cfg.ChatID, "is_from_me": true},
		{"id": "other", "text": "ignore", "chat_id": "other", "is_from_me": false},
	})
	fake := &fakeCommander{results: []CommandResult{{Stdout: payload}}}
	adapter := Adapter{Config: cfg, Commander: fake}
	messages, err := adapter.History(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "incoming" {
		t.Fatalf("messages = %#v", messages)
	}
	want := []string{"history", "--chat-id", cfg.ChatID, "--limit", "20", "--attachments", "--convert-attachments", "--json"}
	if !reflect.DeepEqual(fake.calls[0].args, want) {
		t.Fatalf("args = %#v", fake.calls[0].args)
	}
	if fake.calls[0].name != cfg.ImsgPath {
		t.Fatalf("name = %q", fake.calls[0].name)
	}
}

func TestResponderPassesOnlyTheTurnText(t *testing.T) {
	cfg := testConfig(t)
	cfg.Trusted = true
	cfg.ResponderCwd = t.TempDir()
	fake := &fakeCommander{results: []CommandResult{{Stdout: []byte("done\n")}}}
	adapter := Adapter{Config: cfg, Commander: fake}
	if _, err := adapter.Respond(context.Background(), Message{ID: "m", Text: "launch an agent"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.promptBodies) != 1 || fake.promptBodies[0] != "launch an agent" {
		t.Fatalf("trusted prompt = %#v", fake.promptBodies)
	}
}

func TestPersistentTurnsContainOnlyMessageText(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(fmt.Sprint(cold), func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Trusted = true
			cfg.RouterMode = true
			responder := &fakePersistentResponder{state: PersistentResponderState{NeedsBootstrap: cold}}
			adapter := Adapter{Config: cfg, PersistentResponder: responder}
			if _, err := adapter.RespondMeasured(context.Background(), Message{ID: "42", Text: "hello"}); err != nil {
				t.Fatal(err)
			}
			if responder.prompt != "hello" {
				t.Fatalf("prompt=%q", responder.prompt)
			}
		})
	}
}

func TestRouterModePromptKeepsPolicyInSystemPrompt(t *testing.T) {
	cfg := testConfig(t)
	cfg.Trusted = true
	cfg.RouterMode = true
	responder := &fakePersistentResponder{}
	adapter := Adapter{Config: cfg, PersistentResponder: responder}
	if _, err := adapter.RespondMeasured(context.Background(), Message{ID: "7", Text: "status"}); err != nil {
		t.Fatal(err)
	}
	if responder.prompt != "status" {
		t.Fatalf("turn should contain only user text, got %q", responder.prompt)
	}
}

func TestRouterModeIncrementalPromptKeepsPolicyInSystemPrompt(t *testing.T) {
	cfg := testConfig(t)
	cfg.Trusted = true
	cfg.RouterMode = true
	responder := &fakePersistentResponder{state: PersistentResponderState{NeedsBootstrap: false}}
	adapter := Adapter{Config: cfg, PersistentResponder: responder}
	if _, err := adapter.RespondMeasured(context.Background(), Message{ID: "8", Text: "status"}); err != nil {
		t.Fatal(err)
	}
	if responder.prompt != "status" {
		t.Fatalf("turn should contain only user text, got %q", responder.prompt)
	}
}

func TestTrustedPersistentResponderBudgetCapsExcessiveConfiguredTimeout(t *testing.T) {
	if MaxTrustedResponderDuration != 5*time.Minute {
		t.Fatalf("trusted responder ceiling = %v, want 5m", MaxTrustedResponderDuration)
	}
	cfg := testConfig(t)
	cfg.Trusted = true
	cfg.ResponderTimeoutSeconds = 1200
	adapter := Adapter{Config: cfg, PersistentResponder: &fakePersistentResponder{}}
	if got := adapter.responderTimeout(); got != MaxTrustedResponderDuration {
		t.Fatalf("responder timeout = %v, want %v", got, MaxTrustedResponderDuration)
	}

	cfg.ResponderTimeoutSeconds = 30
	adapter.Config = cfg
	if got := adapter.responderTimeout(); got != 30*time.Second {
		t.Fatalf("short responder timeout = %v, want 30s", got)
	}
}

func TestEmptyPersistentSessionDoesNotInjectMemoryIntoTurn(t *testing.T) {
	cfg := testConfig(t)
	cfg.Trusted = true
	memoryPath := filepath.Join(t.TempDir(), "MEMORY.md")
	if err := os.WriteFile(memoryPath, []byte("durable bootstrap fact"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.MemoryFile = memoryPath
	responder := &fakePersistentResponder{state: PersistentResponderState{NeedsBootstrap: true}}
	adapter := Adapter{Config: cfg, PersistentResponder: responder}
	if _, err := adapter.RespondMeasured(context.Background(), Message{ID: "42", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if responder.prompt != "hello" {
		t.Fatalf("bootstrap turn = %q", responder.prompt)
	}
}

func TestTrustedResponderRetriesTransientProviderFailure(t *testing.T) {
	cfg := testConfig(t)
	cfg.Trusted = true
	fake := &fakeCommander{
		results: []CommandResult{{Stderr: []byte("Provider request failed.")}, {Stdout: []byte("recovered\n")}},
		errors:  []error{errors.New("exit status 1"), nil},
	}
	adapter := Adapter{Config: cfg, Commander: fake}
	reply, err := adapter.Respond(context.Background(), Message{ID: "m", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "recovered" || len(fake.calls) != 2 {
		t.Fatalf("reply = %q, calls = %d", reply, len(fake.calls))
	}
}

func TestDefaultResponderCwdCreatesPrivateOrchestratorDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	dir, err := DefaultResponderCwd()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, "orchestrator") {
		t.Fatalf("responder cwd = %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("orchestrator directory mode = %v", info.Mode())
	}
}

func TestConfigRejectsMissingResponderCwd(t *testing.T) {
	cfg := testConfig(t)
	cfg.ResponderCwd = "/no/such/directory"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "responder cwd") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRespondUsesPrivatePromptFileAndSendPreservesOneArg(t *testing.T) {
	cfg := testConfig(t)
	fake := &fakeCommander{results: []CommandResult{{Stdout: []byte("safe reply\n")}, {Stdout: []byte(`{"ok":true}`)}}}
	adapter := Adapter{Config: cfg, Commander: fake}
	reply, err := adapter.Respond(context.Background(), Message{ID: "m", Text: "$(touch /tmp/nope); --flag"})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "safe reply" {
		t.Fatalf("reply = %q", reply)
	}
	if len(fake.calls[0].args) != 2 || fake.calls[0].args[0] != "--prompt" {
		t.Fatalf("responder args = %#v", fake.calls[0].args)
	}
	promptPath := fake.calls[0].args[1]
	if strings.Contains(promptPath, "touch") {
		t.Fatalf("message leaked into argv: %q", promptPath)
	}
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatalf("prompt file was not removed: %v", err)
	}
	if err := adapter.Send(context.Background(), "- safe reply; $(nope)"); err != nil {
		t.Fatal(err)
	}
	want := []string{"send", "--chat-id", cfg.ChatID, "--text", "\u200b- safe reply; $(nope)", "--json"}
	if !reflect.DeepEqual(fake.calls[1].args, want) {
		t.Fatalf("send args = %#v", fake.calls[1].args)
	}
}

func TestSendUsesPersistentRPCAndPreservesOneTextValue(t *testing.T) {
	cfg := testConfig(t)
	sender := &fakePersistentSender{}
	fake := &fakeCommander{}
	adapter := Adapter{Config: cfg, Commander: fake, PersistentSender: sender}
	if err := adapter.Send(context.Background(), "- safe reply; $(nope)"); err != nil {
		t.Fatal(err)
	}
	if sender.chatID != cfg.ChatID || sender.text != "\u200b- safe reply; $(nope)" {
		t.Fatalf("persistent send = chat %q text %q", sender.chatID, sender.text)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("fallback commander calls = %d, want 0", len(fake.calls))
	}
}

func TestSendFallsBackOnlyWhenRPCIsUnsupported(t *testing.T) {
	cfg := testConfig(t)
	sender := &fakePersistentSender{err: ErrRPCUnsupported}
	fake := &fakeCommander{results: []CommandResult{{Stdout: []byte(`{"ok":true}`)}}}
	adapter := Adapter{Config: cfg, Commander: fake, PersistentSender: sender}
	if err := adapter.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("fallback commander calls = %d, want 1", len(fake.calls))
	}

	sender.err = errors.New("ambiguous RPC failure")
	fake.calls = nil
	if err := adapter.Send(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "ambiguous RPC failure") {
		t.Fatalf("Send() error = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("ambiguous send retried through fallback %d time(s)", len(fake.calls))
	}
}

func TestConfigPrivateAndValidated(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	cfg := testConfig(t)
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	dir, path, _ := Paths()
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0o700}, {path, 0o600}} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != item.mode {
			t.Fatalf("%s mode %o", item.path, info.Mode().Perm())
		}
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestPollIntervalSupportsLegacySecondsAndSubsecondConfig(t *testing.T) {
	legacy := Defaults()
	legacy.PollMilliseconds = 0
	legacy.PollSeconds = 3
	if got := legacy.PollInterval(); got != 3*time.Second {
		t.Fatalf("legacy interval = %s", got)
	}
	current := Defaults()
	if got := current.PollInterval(); got != 250*time.Millisecond {
		t.Fatalf("current interval = %s", got)
	}
}

func TestParseMessagesReadsAttachmentsAndSkipsPluginPayloads(t *testing.T) {
	home, _ := os.UserHomeDir()
	line := `{"id":7,"text":"\uFFFC","is_from_me":false,"chat_id":"1","attachments":[` +
		`{"missing":false,"is_sticker":false,"transfer_name":"IMG_1.heic","filename":"~/Library/Messages/Attachments/a/IMG_1.heic","original_path":"` + home + `/Library/Messages/Attachments/a/IMG_1.heic","mime_type":"image/heic"},` +
		`{"missing":false,"is_sticker":false,"transfer_name":"x.pluginPayloadAttachment","original_path":"/x/x.pluginPayloadAttachment","mime_type":""},` +
		`{"missing":true,"is_sticker":false,"transfer_name":"lost.jpg","original_path":"/x/lost.jpg","mime_type":"image/jpeg"},` +
		`{"missing":false,"is_sticker":false,"transfer_name":"notes.zip","filename":"~/Library/Messages/Attachments/b/notes.zip","mime_type":"application/zip"}]}`
	messages, err := ParseMessages([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	got := messages[0].Attachments
	want := []Attachment{
		{Path: home + "/Library/Messages/Attachments/a/IMG_1.heic", MimeType: "image/heic", Name: "IMG_1.heic"},
		{Path: home + "/Library/Messages/Attachments/b/notes.zip", MimeType: "application/zip", Name: "notes.zip"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
	adapter := Adapter{Config: Config{ChatID: "1", MaxMessageBytes: 4000}}
	if _, ok := adapter.ChatMessage(messages[0]); !ok {
		t.Fatal("an attachment-only message was dropped as empty")
	}
	if _, ok := adapter.ChatMessage(Message{Text: "\uFFFC", ChatID: "1"}); ok {
		t.Fatal("a placeholder with no attachments was accepted")
	}
	prompt := messages[0].PromptText()
	if strings.Contains(prompt, "\uFFFC") || !strings.HasPrefix(prompt, "Image attachment (also provided inline): "+home+"/Library/Messages/Attachments/a/IMG_1.heic (image/heic)\nAttachment: ") {
		t.Fatalf("prompt = %q", prompt)
	}
	captioned := Message{Text: "see this \uFFFC", Attachments: got[:1]}
	if p := captioned.PromptText(); !strings.HasPrefix(p, "see this\nImage attachment") {
		t.Fatalf("captioned prompt = %q", p)
	}
	if p := (Message{Text: "hello"}).PromptText(); p != "hello" {
		t.Fatalf("plain prompt = %q", p)
	}
}
