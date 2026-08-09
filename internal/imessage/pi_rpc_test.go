package imessage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRouterModeStructurallyRestrictsPiAndPreservesPinnedSession(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	cfg := Defaults()
	cfg.Trusted = true
	cfg.RouterMode = true
	cfg.ResponderCwd = t.TempDir()
	cfg.ResponderCommand = []string{"/tmp/pi", "--print", "--session", "/private/original-session.jsonl", "--model", "router/model", "--thinking", "high", "--tools=bash", "--extension", "/tmp/ambient.mjs", "--skill", "/tmp/evil-skill", "--system-prompt=evil", "--append-system-prompt", "/tmp/evil", "--prompt-template=/tmp/template", "@/tmp/context.md", "@{prompt_file}"}
	responder, ok, err := NewPiRPCResponder(cfg)
	if err != nil || !ok {
		t.Fatalf("responder ok=%v err=%v", ok, err)
	}
	joined := strings.Join(responder.argv, " ")
	for _, required := range []string{"--session /private/original-session.jsonl", "--no-builtin-tools", "--no-extensions", "--no-skills", "pi-router-extension.mjs"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("router argv %q missing %q", joined, required)
		}
	}
	if strings.Contains(joined, "--no-tools") {
		t.Fatalf("router argv disabled delegate extension: %q", joined)
	}
	for _, forbidden := range []string{"ambient.mjs", "--tools=bash", "evil-skill", "system-prompt=evil", "/tmp/evil", "/tmp/template", "context.md"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("router retained ambient instruction %q: %q", forbidden, joined)
		}
	}
	for _, preserved := range []string{"--model router/model", "--thinking high", "--session /private/original-session.jsonl", "--no-context-files"} {
		if !strings.Contains(joined, preserved) {
			t.Fatalf("router lost required/tuning flag %q: %q", preserved, joined)
		}
	}
	responder.SetDelegationEnv("http://127.0.0.1:1/v1/delegate", "scoped-cap")
	if strings.Contains(strings.Join(responder.argv, " "), "scoped-cap") {
		t.Fatal("capability leaked into model-visible argv")
	}
}

func TestPiRPCArgvAcceptsEqualsSessionSyntax(t *testing.T) {
	got, ok := piRPCArgv([]string{"/opt/homebrew/bin/pi", "--print", "--session=/private/original-session.jsonl", "@{prompt_file}"})
	if !ok {
		t.Fatal("persistent Pi command was not recognized")
	}
	want := []string{"/opt/homebrew/bin/pi", "--mode", "rpc", "--session", "/private/original-session.jsonl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RPC argv = %#v, want %#v", got, want)
	}
}

func TestPiRPCArgvPreservesPersistentSessionAndRemovesPrintPrompt(t *testing.T) {
	got, ok := piRPCArgv([]string{
		"/opt/homebrew/bin/pi", "--print", "--session-dir", "/tmp/sessions", "--session-id", "orchestrator", "--model", "router/model", "@{prompt_file}",
	})
	if !ok {
		t.Fatal("persistent Pi command was not recognized")
	}
	want := []string{"/opt/homebrew/bin/pi", "--mode", "rpc", "--session-dir", "/tmp/sessions", "--session-id", "orchestrator", "--model", "router/model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RPC argv = %#v, want %#v", got, want)
	}
	if _, ok := piRPCArgv([]string{"/opt/homebrew/bin/pi", "--print", "--no-session", "@{prompt_file}"}); ok {
		t.Fatal("ephemeral Pi command was accepted as persistent RPC")
	}
}

func TestNewPiRPCResponderAddsPrivateContextFilter(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	cfg := Defaults()
	cfg.Trusted = true
	cfg.ResponderCommand = []string{"/tmp/pi", "--print", "--session-id", "orchestrator", "@{prompt_file}"}
	responder, ok, err := NewPiRPCResponder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("trusted persistent Pi command was not recognized")
	}
	if got := responder.argv[len(responder.argv)-2:]; !reflect.DeepEqual(got, []string{"--extension", responder.contextFilterPath}) {
		t.Fatalf("extension args = %#v", got)
	}
	if err := writePrivateAsset(responder.contextFilterPath, piContextFilter); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(responder.contextFilterPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("context filter mode = %o", info.Mode().Perm())
	}
}

func TestPiRPCResponderReusesOneWarmProcess(t *testing.T) {
	responder := &PiRPCResponder{
		argv: []string{os.Args[0], "-test.run=TestPiRPCHelperProcess"},
		env:  append(os.Environ(), "CONTEXT_DROP_PI_RPC_HELPER=1", "CONTEXT_DROP_PI_RPC_MESSAGE_COUNT=2"),
	}
	defer responder.Close()

	state, err := responder.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.ColdStart || state.NeedsBootstrap {
		t.Fatalf("initial state = %#v", state)
	}
	warm, err := responder.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if warm.ColdStart || warm.NeedsBootstrap {
		t.Fatalf("warm state = %#v", warm)
	}

	first, err := responder.Respond(context.Background(), "first", 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := responder.Respond(context.Background(), "second", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reply != "reply 1" || second.Reply != "reply 2" {
		t.Fatalf("replies = %q, %q", first.Reply, second.Reply)
	}
	if first.Metrics.TimeToFirstOutput <= 0 || first.Metrics.Responder <= 0 {
		t.Fatalf("metrics = %#v", first.Metrics)
	}
	if len(first.Metrics.ModelRounds) != 1 || first.Metrics.ModelRounds[0].Model != "fast/model" || first.Metrics.ModelRounds[0].ResponseID != "response-1" {
		t.Fatalf("model rounds = %#v", first.Metrics.ModelRounds)
	}
}

func TestPiRPCResponderAbortsTimedOutTurnAndRemainsWarm(t *testing.T) {
	responder := &PiRPCResponder{
		argv: []string{os.Args[0], "-test.run=TestPiRPCHelperProcess"},
		env:  append(os.Environ(), "CONTEXT_DROP_PI_RPC_HELPER=1", "CONTEXT_DROP_PI_RPC_MESSAGE_COUNT=2", "CONTEXT_DROP_PI_RPC_HANG_FIRST=1"),
	}
	defer responder.Close()
	if _, err := responder.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := responder.Respond(ctx, "hang", 1024); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("timed out response error = %v", err)
	}
	if responder.cmd == nil {
		t.Fatal("cooperatively aborted responder was discarded")
	}

	response, err := responder.Respond(context.Background(), "next", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if response.Reply != "reply 2" {
		t.Fatalf("reply after abort = %q", response.Reply)
	}
}

func TestPiRPCHelperProcess(t *testing.T) {
	if os.Getenv("CONTEXT_DROP_PI_RPC_HELPER") != "1" {
		return
	}
	messageCount, _ := strconv.Atoi(os.Getenv("CONTEXT_DROP_PI_RPC_MESSAGE_COUNT"))
	dec := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	prompts := 0
	for dec.Scan() {
		var command struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(dec.Bytes(), &command) != nil {
			continue
		}
		switch command.Type {
		case "get_state":
			_ = enc.Encode(map[string]any{"id": command.ID, "type": "response", "command": "get_state", "success": true, "data": map[string]any{"messageCount": messageCount}})
		case "abort":
			_ = enc.Encode(map[string]any{"id": command.ID, "type": "response", "command": "abort", "success": true})
			_ = enc.Encode(map[string]any{"type": "agent_settled"})
		case "prompt":
			prompts++
			_ = enc.Encode(map[string]any{"id": command.ID, "type": "response", "command": "prompt", "success": true})
			if prompts == 1 && os.Getenv("CONTEXT_DROP_PI_RPC_HANG_FIRST") == "1" {
				continue
			}
			text := fmt.Sprintf("reply %d", prompts)
			assistant := map[string]any{"role": "assistant", "model": "router", "responseModel": "fast/model", "responseId": fmt.Sprintf("response-%d", prompts), "usage": map[string]any{"totalTokens": 42}, "content": []map[string]any{{"type": "text", "text": text}}}
			_ = enc.Encode(map[string]any{"type": "turn_start"})
			_ = enc.Encode(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": text}})
			_ = enc.Encode(map[string]any{"type": "message_end", "message": assistant})
			_ = enc.Encode(map[string]any{"type": "turn_end", "message": assistant})
			_ = enc.Encode(map[string]any{"type": "agent_settled"})
		}
	}
	os.Exit(0)
}
