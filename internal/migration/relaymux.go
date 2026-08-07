package migration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type RelaymuxInventory struct {
	Home      string              `json:"home"`
	Config    LegacyConfig        `json:"config"`
	Schedules []LegacySchedule    `json:"schedules"`
	Launchd   []LegacyLaunchAgent `json:"launchd"`
	Counts    LegacyCounts        `json:"counts"`
	DataPaths []LegacyDataPath    `json:"data_paths"`
	Blockers  []string            `json:"unsupported_blockers"`
}
type LegacyConfig struct {
	Exists                  bool     `json:"exists"`
	Version                 int      `json:"version,omitempty"`
	SessionConfigured       bool     `json:"session_configured"`
	Agents                  []string `json:"agents,omitempty"`
	IMessageConfigured      bool     `json:"imessage_configured"`
	OrchestratorConfigured  bool     `json:"orchestrator_configured"`
	SensitiveValuesRedacted bool     `json:"sensitive_values_redacted"`
}
type LegacySchedule struct {
	Name      string `json:"name"`
	Cron      string `json:"cron,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Installed bool   `json:"installed"`
}
type LegacyLaunchAgent struct {
	Label   string `json:"label"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Loaded  bool   `json:"loaded"`
}
type LegacyCounts struct {
	Runs   int `json:"runs"`
	Events int `json:"events"`
}
type LegacyDataPath struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Exists         bool   `json:"exists"`
	Bytes          int64  `json:"bytes"`
	SizeIncomplete bool   `json:"size_incomplete,omitempty"`
}

func InspectRelaymux(home string) (RelaymuxInventory, error) {
	if home == "" {
		return RelaymuxInventory{}, fmt.Errorf("--home is required")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return RelaymuxInventory{}, err
	}
	inventory := RelaymuxInventory{Home: abs, Blockers: []string{
		"persistent orchestrator ask/notify and reply-mode behavior is not supported",
		"legacy run history cannot yet be imported as native Context Drop runs",
		"worktree creation and steering existing agent sessions are not supported",
	}}
	if err := inspectConfig(filepath.Join(abs, "config.json"), &inventory.Config); err != nil {
		return inventory, err
	}
	inventory.Schedules, err = inspectSchedules(filepath.Join(abs, "state", "schedules"))
	if err != nil {
		return inventory, err
	}
	inventory.Launchd, err = inspectLaunchAgents(abs)
	if err != nil {
		return inventory, err
	}
	inventory.Counts.Runs, err = countLines(filepath.Join(abs, "state", "runs.jsonl"))
	if err != nil {
		return inventory, err
	}
	inventory.Counts.Events, err = countLines(filepath.Join(abs, "state", "events.jsonl"))
	if err != nil {
		return inventory, err
	}
	for _, item := range []struct{ name, rel string }{
		{"database", "relaymux.sqlite3"}, {"state", "state"}, {"logs", "logs"}, {"tasks", "tasks"}, {"reports", "reports"}, {"artifacts", "artifacts"},
	} {
		path := filepath.Join(abs, item.rel)
		size, exists, incomplete, sizeErr := pathSize(path)
		if sizeErr != nil {
			return inventory, sizeErr
		}
		inventory.DataPaths = append(inventory.DataPaths, LegacyDataPath{Name: item.name, Path: path, Exists: exists, Bytes: size, SizeIncomplete: incomplete})
	}
	return inventory, nil
}

func inspectConfig(path string, out *LegacyConfig) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw struct {
		Version      int                        `json:"version"`
		Session      string                     `json:"session"`
		Agents       map[string]json.RawMessage `json:"agents"`
		IMessage     json.RawMessage            `json:"imessage"`
		Orchestrator json.RawMessage            `json:"orchestrator"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("read legacy config: %w", err)
	}
	out.Exists, out.Version, out.SessionConfigured = true, raw.Version, raw.Session != ""
	for name := range raw.Agents {
		out.Agents = append(out.Agents, name)
	}
	sort.Strings(out.Agents)
	out.IMessageConfigured = len(raw.IMessage) > 0 && string(raw.IMessage) != "null"
	out.OrchestratorConfigured = len(raw.Orchestrator) > 0 && string(raw.Orchestrator) != "null"
	out.SensitiveValuesRedacted = true
	return nil
}

func inspectSchedules(root string) ([]LegacySchedule, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LegacySchedule
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "schedule.json")
		data, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		var raw struct {
			Name      string `json:"name"`
			Cron      string `json:"cron"`
			Timezone  string `json:"timezone"`
			Installed bool   `json:"installed"`
		}
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		if raw.Name == "" {
			raw.Name = entry.Name()
		}
		out = append(out, LegacySchedule{Name: raw.Name, Cron: raw.Cron, Timezone: raw.Timezone, Installed: raw.Installed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func inspectLaunchAgents(_ string) ([]LegacyLaunchAgent, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(userHome, "Library", "LaunchAgents")
	matches, err := filepath.Glob(filepath.Join(dir, "*relaymux*.plist"))
	if err != nil {
		return nil, err
	}
	out := make([]LegacyLaunchAgent, 0, len(matches))
	for _, path := range matches {
		label := strings.TrimSuffix(filepath.Base(path), ".plist")
		loaded := false
		if runtime.GOOS == "darwin" {
			loaded = exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run() == nil
		}
		out = append(out, LegacyLaunchAgent{Label: label, Path: path, Present: true, Loaded: loaded})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func countLines(path string) (int, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}
func pathSize(path string) (int64, bool, bool, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return 0, false, false, nil
	} else if err != nil {
		return 0, false, false, err
	}
	var size int64
	incomplete := false
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				incomplete = true
				return fs.SkipDir
			}
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if os.IsPermission(infoErr) {
			incomplete = true
			return nil
		}
		if infoErr != nil {
			return infoErr
		}
		size += info.Size()
		return nil
	})
	return size, true, incomplete, err
}
