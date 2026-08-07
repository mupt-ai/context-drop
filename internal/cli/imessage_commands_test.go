package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/imessage"
)

func TestIMessageSetupSavesWithoutExecuting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	logPath := filepath.Join(home, "executed")
	fake := filepath.Join(home, "imsg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho called >"+logPath+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	responder := filepath.Join(home, "responder")
	if err := os.WriteFile(responder, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := newIMessageCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"setup", "--chat-id", "1;nope", "--imsg-path", fake, "--responder-arg", responder, "--responder-arg", "{prompt_file}"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("setup executed imsg")
	}
	cfg, err := imessage.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatID != "1;nope" || filepath.Base(cfg.ImsgPath) != filepath.Base(fake) || len(cfg.ResponderCommand) != 2 {
		t.Fatalf("config = %#v", cfg)
	}
	if !strings.Contains(out.String(), "No message was sent") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestIMessageStatusJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	fake := filepath.Join(home, "fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.ChatID = "1"
	cfg.ImsgPath = fake
	cfg.ResponderCommand = []string{fake, "{prompt_file}"}
	if err := imessage.Save(cfg); err != nil {
		t.Fatal(err)
	}
	cmd := newIMessageCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["chat_id"] != "1" || decoded["enabled"] != true {
		t.Fatalf("status = %#v", decoded)
	}
}
