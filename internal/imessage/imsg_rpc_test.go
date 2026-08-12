package imessage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestIMsgRPCSenderReusesOneProcess(t *testing.T) {
	starts := t.TempDir() + "/starts"
	sender := &IMsgRPCSender{
		argv: []string{os.Args[0], "-test.run=TestIMsgRPCHelperProcess"},
		env:  append(os.Environ(), "CONTEXT_DROP_IMSG_RPC_HELPER=1", "CONTEXT_DROP_IMSG_RPC_STARTS="+starts),
	}
	defer sender.Close()

	if err := sender.Send(context.Background(), "1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), "1", "second"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "started\n"); got != 1 {
		t.Fatalf("process starts = %d, want 1", got)
	}
}

func TestIMsgRPCSenderReportsUnsupportedCommand(t *testing.T) {
	path := writeWatchFixture(t, "echo 'Error: unknown command rpc' >&2\nexit 2\n")
	sender := &IMsgRPCSender{argv: []string{path, "rpc"}}
	err := sender.Send(context.Background(), "1", "hello")
	if !errors.Is(err, ErrRPCUnsupported) {
		t.Fatalf("Send() error = %v", err)
	}
	if err := sender.Send(context.Background(), "1", "hello"); !errors.Is(err, ErrRPCUnsupported) {
		t.Fatalf("second Send() error = %v", err)
	}
}

func TestIMsgRPCHelperProcess(t *testing.T) {
	if os.Getenv("CONTEXT_DROP_IMSG_RPC_HELPER") != "1" {
		return
	}
	starts := os.Getenv("CONTEXT_DROP_IMSG_RPC_STARTS")
	file, err := os.OpenFile(starts, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = file.WriteString("started\n")
	_ = file.Close()

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params struct {
				ChatID string `json:"chat_id"`
				Text   string `json:"text"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if request.Method != "send" || request.Params.ChatID != "1" || request.Params.Text == "" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32602, "message": "bad request"}})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"ok": true}})
	}
	os.Exit(0)
}
