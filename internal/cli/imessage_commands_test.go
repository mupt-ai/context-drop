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
