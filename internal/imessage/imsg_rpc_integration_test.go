//go:build integration

package imessage

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// This opt-in test sends real messages through one installed imsg RPC process.
// It requires an explicit allow flag because it mutates the configured chat.
func TestIMsgRPCSenderInstalledBinary(t *testing.T) {
	if os.Getenv("CONTEXT_DROP_IMSG_ALLOW_SEND") != "1" {
		t.Skip("set CONTEXT_DROP_IMSG_ALLOW_SEND=1 to allow real message sends")
	}
	path := os.Getenv("CONTEXT_DROP_IMSG_PATH")
	chatID := os.Getenv("CONTEXT_DROP_IMSG_CHAT_ID")
	if path == "" || chatID == "" {
		t.Skip("set CONTEXT_DROP_IMSG_PATH and CONTEXT_DROP_IMSG_CHAT_ID")
	}
	count := 3
	if value := os.Getenv("CONTEXT_DROP_IMSG_SEND_COUNT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 20 {
			t.Fatalf("invalid CONTEXT_DROP_IMSG_SEND_COUNT %q", value)
		}
		count = parsed
	}

	sender := NewIMsgRPCSender(Config{ImsgPath: path})
	defer sender.Close()
	for index := 1; index <= count; index++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		started := time.Now()
		err := sender.Send(ctx, chatID, fmt.Sprintf("Context Drop persistent-send latency test %d/%d", index, count))
		duration := time.Since(started)
		cancel()
		if err != nil {
			t.Fatalf("send %d: %v", index, err)
		}
		t.Logf("send=%d duration=%s", index, duration)
	}
}
