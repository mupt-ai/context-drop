package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRelaymuxRedactsAndInventories(t *testing.T) {
	home := t.TempDir()
	config := `{"version":1,"session":"dari","imessage":{"recipient":"+15551234567","token":"secret"},"agents":{"pi":{"command":["pi","key-secret"]}},"orchestrator":{"apiKey":"secret"}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleDir := filepath.Join(home, "state", "schedules", "digest")
	if err := os.MkdirAll(scheduleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scheduleDir, "schedule.json"), []byte(`{"name":"digest","cron":"0 8,13,19 * * *","timezone":"America/Los_Angeles","installed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "runs.jsonl"), []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := InspectRelaymux(home)
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Config.SensitiveValuesRedacted || len(inventory.Config.Agents) != 1 || inventory.Config.Agents[0] != "pi" {
		t.Fatalf("config = %#v", inventory.Config)
	}
	if len(inventory.Schedules) != 1 || inventory.Schedules[0].Cron != "0 8,13,19 * * *" || inventory.Counts.Runs != 2 {
		t.Fatalf("inventory = %#v", inventory)
	}
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"+15551234567", "key-secret", "apiKey"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("inventory leaked %q: %s", secret, data)
		}
	}
}

func TestInspectRelaymuxDoesNotCreateHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	if _, err := InspectRelaymux(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("inspect mutated missing home: %v", err)
	}
}
