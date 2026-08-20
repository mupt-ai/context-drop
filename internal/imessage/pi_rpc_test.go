package imessage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	session := filepath.Join(t.TempDir(), "original-session.jsonl")
	if err := os.WriteFile(session, []byte(fmt.Sprintf("{\"type\":\"session\",\"cwd\":%q}\n", cfg.ResponderCwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.ResponderCommand = []string{"/tmp/pi", "--print", "--session", session, "--model", "router/model", "--thinking", "high", "--tools=bash", "--extension", "/tmp/ambient.mjs", "--skill", "/tmp/evil-skill", "--system-prompt=evil", "--append-system-prompt", "/tmp/evil", "--prompt-template=/tmp/template", "@/tmp/context.md", "@{prompt_file}"}
	responder, ok, err := NewPiRPCResponder(cfg)
	if err != nil || !ok {
		t.Fatalf("responder ok=%v err=%v", ok, err)
	}
	joined := strings.Join(responder.argv, " ")
	for _, required := range []string{"--session " + session, "--no-builtin-tools", "--no-extensions", "--no-skills", "pi-router-extension.mjs"} {
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
	for _, preserved := range []string{"--model router/model", "--thinking high", "--session " + session, "--no-context-files"} {
		if !strings.Contains(joined, preserved) {
			t.Fatalf("router lost required/tuning flag %q: %q", preserved, joined)
		}
	}
	responder.SetDelegationEnv("http://127.0.0.1:1/v1/tasks/delegate", "scoped-cap")
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

func TestPiRPCResponderRestartsAfterDelegationEnvRotationBetweenPrepareAndRespond(t *testing.T) {
	responder := &PiRPCResponder{
		argv: []string{os.Args[0], "-test.run=TestPiRPCHelperProcess"},
		env:  append(os.Environ(), "CONTEXT_DROP_PI_RPC_HELPER=1", "CONTEXT_DROP_PI_RPC_MESSAGE_COUNT=2"),
	}
	defer responder.Close()

	prepared, err := responder.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.ColdStart {
		t.Fatalf("prepare state = %#v", prepared)
	}

	// This is the production race: capability rotation stops the process after
	// Adapter has released Prepare's turn gate but before Respond acquires it.
	responder.SetDelegationEnv("http://127.0.0.1:1/v1/tasks/delegate", "rotated-capability")
	response, err := responder.Respond(context.Background(), "after rotation", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if response.Reply != "reply 1" {
		t.Fatalf("reply = %q", response.Reply)
	}
	if !response.Metrics.ColdStart || response.Metrics.ResponderStartup <= 0 {
		t.Fatalf("restart metrics = %#v", response.Metrics)
	}
	if url, capability := responder.DelegationEnv(); url == "" || capability != "rotated-capability" {
		t.Fatalf("delegation env = %q, %q", url, capability)
	}
}

func TestPiRPCResponderAdmissionIsContextCancellable(t *testing.T) {
	responder := &PiRPCResponder{}
	if err := responder.acquireTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := responder.Respond(ctx, "waiting", 1024)
	responder.releaseTurn()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("waiting response error=%v duration=%s", err, time.Since(started))
	}
}

func TestRecoverMissingSessionCwdRotatesSessionID(t *testing.T) {
	fallback := t.TempDir()
	session := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(session, []byte(`{"type":"session","version":3,"cwd":"/definitely/missing/context-drop-cwd"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := recoverMissingSessionCwd([]string{"pi", "--mode", "rpc", "--session", session, "--model", "x"}, fallback)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, session) || !strings.Contains(joined, "--session-id context-drop-recovered-") {
		t.Fatalf("recovered argv=%q", joined)
	}
}

func TestRecoverMissingSessionCwdRejectsMissingSessionFile(t *testing.T) {
	_, err := recoverMissingSessionCwd([]string{"pi", "--session", filepath.Join(t.TempDir(), "missing.jsonl")}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "session file does not exist") {
		t.Fatalf("error=%v", err)
	}
}

func TestRecoverMissingSessionCwdReturnsNonNotExistStatError(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(session, []byte(fmt.Sprintf("{\"type\":\"session\",\"cwd\":%q}\n", loop)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := recoverMissingSessionCwd([]string{"pi", "--session", session}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "stat persistent Pi session cwd") {
		t.Fatalf("error=%v", err)
	}
}

func TestRecoverMissingSessionCwdRejectsUnresolvedSessionID(t *testing.T) {
	_, err := recoverMissingSessionCwd([]string{"pi", "--session", "orchestrator"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "explicit JSONL path") {
		t.Fatalf("error=%v", err)
	}
	sessionDir := t.TempDir()
	got, err := recoverMissingSessionCwd([]string{"pi", "--session-dir", sessionDir, "--session-id", "orchestrator"}, t.TempDir())
	if err != nil || !reflect.DeepEqual(got, []string{"pi", "--session-dir", sessionDir, "--session-id", "orchestrator"}) {
		t.Fatalf("argv=%q error=%v", got, err)
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

func TestPiRPCResponderPreservesSuccessfulToolEvidenceOnEmptyFinal(t *testing.T) {
	responder := &PiRPCResponder{argv: []string{os.Args[0], "-test.run=TestPiRPCHelperProcess"}, env: append(os.Environ(), "CONTEXT_DROP_PI_RPC_HELPER=1", "CONTEXT_DROP_PI_RPC_MESSAGE_COUNT=2", "CONTEXT_DROP_PI_RPC_EMPTY_AFTER_TOOL=1")}
	defer responder.Close()
	if _, err := responder.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := responder.Respond(context.Background(), "delegate", 1024)
	if err == nil || !response.ToolCompleted || !response.SideEffectToolCompleted {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestPiRPCResponderDoesNotCountFailedSideEffectTool(t *testing.T) {
	responder := &PiRPCResponder{argv: []string{os.Args[0], "-test.run=TestPiRPCHelperProcess"}, env: append(os.Environ(), "CONTEXT_DROP_PI_RPC_HELPER=1", "CONTEXT_DROP_PI_RPC_MESSAGE_COUNT=2", "CONTEXT_DROP_PI_RPC_EMPTY_AFTER_TOOL=1", "CONTEXT_DROP_PI_RPC_TOOL_ERROR=1")}
	defer responder.Close()
	if _, err := responder.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := responder.Respond(context.Background(), "delegate", 1024)
	if err == nil || response.ToolCompleted || response.SideEffectToolCompleted {
		t.Fatalf("response=%#v err=%v", response, err)
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
			if os.Getenv("CONTEXT_DROP_PI_RPC_EMPTY_AFTER_TOOL") == "1" {
				_ = enc.Encode(map[string]any{"type": "tool_execution_start", "toolCallId": "tool-1", "toolName": "delegate_task"})
				_ = enc.Encode(map[string]any{"type": "tool_execution_end", "toolCallId": "tool-1", "toolName": "delegate_task", "isError": os.Getenv("CONTEXT_DROP_PI_RPC_TOOL_ERROR") == "1"})
				_ = enc.Encode(map[string]any{"type": "agent_settled"})
				continue
			}
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
