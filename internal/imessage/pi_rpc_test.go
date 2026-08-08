package imessage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
)

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
		case "prompt":
			prompts++
			text := fmt.Sprintf("reply %d", prompts)
			assistant := map[string]any{"role": "assistant", "model": "router", "responseModel": "fast/model", "responseId": fmt.Sprintf("response-%d", prompts), "usage": map[string]any{"totalTokens": 42}, "content": []map[string]any{{"type": "text", "text": text}}}
			_ = enc.Encode(map[string]any{"id": command.ID, "type": "response", "command": "prompt", "success": true})
			_ = enc.Encode(map[string]any{"type": "turn_start"})
			_ = enc.Encode(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": text}})
			_ = enc.Encode(map[string]any{"type": "message_end", "message": assistant})
			_ = enc.Encode(map[string]any{"type": "turn_end", "message": assistant})
			_ = enc.Encode(map[string]any{"type": "agent_settled"})
		}
	}
	os.Exit(0)
}
