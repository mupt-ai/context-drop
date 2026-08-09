package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
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
	if cfg.ChatID != "1;nope" || cfg.YoloMode || filepath.Base(cfg.ImsgPath) != filepath.Base(fake) || len(cfg.ResponderCommand) != 2 {
		t.Fatalf("config = %#v", cfg)
	}
	if !strings.Contains(out.String(), "No message was sent") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestIMessageRouterModeRequiresConfiguredDelegateAgent(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	cmd := newIMessageCommand()
	cmd.SetArgs([]string{"setup", "--router-mode", "--trusted", "--chat-id", "chat"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "configured delegateAgent") {
		t.Fatalf("err = %v", err)
	}
}

func writeRouterSetupFixture(t *testing.T, home string) (string, string) {
	t.Helper()
	fake := func(name string) string {
		path := filepath.Join(home, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	imsgPath, piPath := fake("imsg"), fake("pi")
	nodePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(home, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeclient.RuntimeConfig{
		Host: "127.0.0.1", Port: 47762, StateDir: runtimeDir,
		TokenFile: filepath.Join(runtimeDir, "token"), NodePath: nodePath,
		DefaultBackend: "tmux", TmuxSession: "context-drop", HerdrSession: "default",
		Agents:        map[string]runtimeclient.AgentConfig{"pi": {Command: []string{piPath, "@{prompt_file}"}, PromptMode: "arg"}},
		DelegateAgent: "pi",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return imsgPath, piPath
}

func TestIMessageRouterModeCreatesAndPinsDurableSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	imsgPath, _ := writeRouterSetupFixture(t, home)
	cmd := newIMessageCommand()
	cmd.SetArgs([]string{"setup", "--router-mode", "--trusted", "--chat-id", "chat", "--imsg-path", imsgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, err := imessage.Load()
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(home, "sessions", "imessage.jsonl")
	joined := strings.Join(cfg.ResponderCommand, "\x00")
	if !strings.Contains(joined, "--session\x00"+sessionPath) || strings.Contains(joined, "--no-session") {
		t.Fatalf("responder command does not pin durable session: %#v", cfg.ResponderCommand)
	}
	info, err := os.Stat(sessionPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("session info=%v err=%v", info, err)
	}
}

func TestIMessageYoloModePersistsOnlyWhenExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	imsgPath, _ := writeRouterSetupFixture(t, home)
	cmd := newIMessageCommand()
	cmd.SetArgs([]string{"setup", "--router-mode", "--yolo-mode", "--trusted", "--chat-id", "chat", "--imsg-path", imsgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, err := imessage.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.YoloMode {
		t.Fatalf("yolo mode not persisted: %#v", cfg)
	}
	bad := newIMessageCommand()
	bad.SetArgs([]string{"setup", "--yolo-mode", "--trusted", "--chat-id", "chat", "--imsg-path", imsgPath})
	if err := bad.Execute(); err == nil || !strings.Contains(err.Error(), "requires --router-mode") {
		t.Fatalf("err=%v", err)
	}
}

func TestIMessageRouterModePreservesExplicitPinnedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	imsgPath, piPath := writeRouterSetupFixture(t, home)
	sessionPath := filepath.Join(home, "original-session.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newIMessageCommand()
	cmd.SetArgs([]string{"setup", "--router-mode", "--trusted", "--chat-id", "chat", "--imsg-path", imsgPath, "--responder-arg", piPath, "--responder-arg=--session", "--responder-arg", sessionPath, "--responder-arg=@{prompt_file}"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, err := imessage.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.ResponderCommand, "\x00"); got != strings.Join([]string{piPath, "--session", sessionPath, "@{prompt_file}"}, "\x00") {
		t.Fatalf("explicit pinned session changed: %#v", cfg.ResponderCommand)
	}
	if err := validatePinnedRouterSession([]string{piPath, "@{prompt_file}"}); err == nil || !strings.Contains(err.Error(), "--session-file") {
		t.Fatalf("missing-session error = %v", err)
	}
}

func TestIMessageLatencyReportUsesLatestInstrumentedSentSample(t *testing.T) {
	jobs := map[string]orchestrator.MessageJob{}
	base := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		created := base.Add(time.Duration(i) * time.Minute)
		sent := created.Add(2 * time.Second)
		jobs[fmt.Sprintf("message-%02d", i)] = orchestrator.MessageJob{
			MessageID: fmt.Sprintf("message-%02d", i),
			Status:    "sent",
			SentAt:    &sent,
			Latency: orchestrator.MessageLatency{
				MessageCreatedAt: &created,
				EndToEndMS:       int64(1000 + i*100),
				QueueMS:          100,
				ServiceMS:        int64(900 + i*100),
			},
		}
	}
	report, err := buildIMessageLatencyReport(jobs, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if report.SampleSize != 20 || report.Metrics["end_to_end"].P50MS != 2400 || report.Metrics["end_to_end"].P90MS != 3200 {
		t.Fatalf("report = %#v", report)
	}
	if !report.Target.Met {
		t.Fatalf("target should be met: %#v", report.Target)
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
