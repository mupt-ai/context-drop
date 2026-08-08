//go:build integration

package imessage

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// This opt-in test reads the local Messages database through an installed imsg
// watcher. It never sends a message or changes the database.
func TestExecMessageWatcherInstalledBinary(t *testing.T) {
	path := os.Getenv("CONTEXT_DROP_IMSG_PATH")
	chatID := os.Getenv("CONTEXT_DROP_IMSG_CHAT_ID")
	sinceText := os.Getenv("CONTEXT_DROP_IMSG_SINCE_ROWID")
	if path == "" || chatID == "" || sinceText == "" {
		t.Skip("set CONTEXT_DROP_IMSG_PATH, CONTEXT_DROP_IMSG_CHAT_ID, and CONTEXT_DROP_IMSG_SINCE_ROWID")
	}
	sinceRowID, err := strconv.ParseInt(sinceText, 10, 64)
	if err != nil || sinceRowID < 0 {
		t.Fatalf("invalid cursor %q", sinceText)
	}
	cfg := Defaults()
	cfg.ChatID = chatID
	cfg.ImsgPath = path
	watcher := ExecMessageWatcher{Config: cfg}
	stop := errors.New("observed installed watcher event")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var observed Message
	err = watcher.Watch(ctx, sinceRowID, func(message Message) error {
		observed = message
		return stop
	})
	if errors.Is(err, context.DeadlineExceeded) {
		t.Log("watcher remained healthy for five seconds; no row followed the supplied cursor")
		return
	}
	if !errors.Is(err, stop) {
		t.Fatalf("Watch() error = %v", err)
	}
	if observed.ID == "" || observed.ChatID != chatID {
		t.Fatalf("message = %#v", observed)
	}
	t.Logf("observed rowid=%s from_me=%t", observed.ID, observed.FromMe)
}
