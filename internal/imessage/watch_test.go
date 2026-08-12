package imessage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeWatchFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "imsg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func watchTestConfig(path string) Config {
	cfg := Defaults()
	cfg.ChatID = "1"
	cfg.ImsgPath = path
	return cfg
}

func TestExecMessageWatcherStreamsFromExclusiveCursor(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("WATCH_ARGS_PATH", argsPath)
	path := writeWatchFixture(t, `printf '%s\n' "$@" > "$WATCH_ARGS_PATH"
printf '%s\n' '{"id":42,"chat_id":1,"text":"hello","created_at":"2026-08-08T00:00:00Z","is_from_me":false}'
`)
	watcher := ExecMessageWatcher{Config: watchTestConfig(path)}
	stop := errors.New("stop after message")
	var got Message
	err := watcher.Watch(context.Background(), 41, func(message Message) error {
		got = message
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Watch() error = %v", err)
	}
	if got.ID != "42" || got.ChatID != "1" || got.Text != "hello" || got.FromMe {
		t.Fatalf("message = %#v", got)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"watch", "--chat-id", "1", "--debounce", "250ms", "--attachments", "--convert-attachments", "--json", "--since-rowid", "41"}
	if !slices.Equal(args, want) {
		t.Fatalf("watch args = %q, want %q", args, want)
	}
}

func TestExecMessageWatcherReportsUnsupportedCommand(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "stderr", body: "echo 'Error: unknown command watch' >&2\nexit 2\n"},
		{name: "stdout", body: "echo 'Error: unrecognized subcommand watch'\nexit 2\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeWatchFixture(t, test.body)
			watcher := ExecMessageWatcher{Config: watchTestConfig(path)}
			err := watcher.Watch(context.Background(), 0, func(Message) error { return nil })
			if !errors.Is(err, ErrWatchUnsupported) {
				t.Fatalf("Watch() error = %v", err)
			}
		})
	}
}

func TestExecMessageWatcherStopsWithContext(t *testing.T) {
	path := writeWatchFixture(t, "exec /bin/sleep 60\n")
	watcher := ExecMessageWatcher{Config: watchTestConfig(path)}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := watcher.Watch(ctx, 0, func(Message) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Watch() error = %v", err)
	}
}
