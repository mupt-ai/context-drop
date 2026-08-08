package imessage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	cfg.ChatID = "chat;$(nope)"
	cfg.ImsgPath = executable
	cfg.ResponderCommand = []string{executable, "--prompt", "{prompt_file}"}
	return cfg
}

func TestParseMessagesJSONAndJSONL(t *testing.T) {
	inputs := [][]byte{
		[]byte(`{"messages":[{"guid":"a","text":"hello","is_from_me":false,"chat_id":"1"},{"id":2,"body":"sent","direction":"outgoing"}]}`),
		[]byte("{\"id\":\"a\",\"text\":\"hello\",\"chat_id\":\"1\"}\n{\"id\":2,\"body\":\"sent\",\"direction\":\"outgoing\"}\n"),
	}
	for _, input := range inputs {
		messages, err := ParseMessages(input)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 2 || messages[0].ID != "a" || messages[0].Text != "hello" || messages[0].ChatID != "1" || messages[0].FromMe || !messages[1].FromMe {
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

func TestTrustedResponderPromptEnablesOrchestration(t *testing.T) {
	cfg := testConfig(t)
	cfg.Trusted = true
	cfg.ResponderCwd = t.TempDir()
	fake := &fakeCommander{results: []CommandResult{{Stdout: []byte("done\n")}}}
	adapter := Adapter{Config: cfg, Commander: fake}
	if _, err := adapter.Respond(context.Background(), Message{ID: "m", Text: "launch an agent"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.promptBodies) != 1 || !strings.Contains(fake.promptBodies[0], "persistent coding orchestrator") {
		t.Fatalf("trusted prompt = %#v", fake.promptBodies)
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
