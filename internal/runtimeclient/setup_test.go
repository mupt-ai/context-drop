package runtimeclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializeHonorsPortAndPrivateModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	t.Setenv("CONTEXT_DROP_RUNTIME_PORT", "49123")
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 49123 || cfg.Host != "127.0.0.1" || cfg.DefaultBackend != "herdr" || cfg.HerdrSession != "default" {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.NodePath == "" || !filepath.IsAbs(cfg.NodePath) {
		t.Fatalf("NodePath was not persisted: %q", cfg.NodePath)
	}
	if _, err := os.Stat(cfg.NodePath); err != nil {
		t.Fatalf("NodePath is not executable: %v", err)
	}
	dir, configPath, tokenPath, _ := Paths()
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0o700}, {configPath, 0o600}, {tokenPath, 0o600}} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != tc.mode {
			t.Fatalf("%s mode = %o", filepath.Base(tc.path), info.Mode().Perm())
		}
	}
}

func TestInitializeMigratesPiPromptFileSyntax(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	_, configPath, _, _ := Paths()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents["pi"] = AgentConfig{Command: []string{"/opt/homebrew/bin/pi", "{prompt_file}"}, PromptMode: "arg"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agents["pi"].Command[1]; got != "@{prompt_file}" {
		t.Fatalf("Pi prompt argument = %q", got)
	}
}

func TestInitializeRejectsInvalidPort(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	for _, value := range []string{"nope", "0", "65536"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CONTEXT_DROP_RUNTIME_PORT", value)
			if _, err := Initialize(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestInitializeUsesPersistedNodePathWhenPATHHasNoNode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	nodePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, configPath, _, err := Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(RuntimeConfig{Host: "127.0.0.1", Port: 47762, NodePath: nodePath, DefaultBackend: "tmux", TmuxSession: "context-drop", Agents: map[string]AgentConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodePath != nodePath {
		t.Fatalf("NodePath = %q, want %q", cfg.NodePath, nodePath)
	}
}

func TestInitializeHonorsBackendEnvOverrides(t *testing.T) {
	home := t.TempDir()
	nodePath, err := ResolveExecutable("node")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(home, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nodePath, filepath.Join(binDir, "node")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("CONTEXT_DROP_HOME", home)
	t.Setenv("CONTEXT_DROP_BACKEND", "herdr")
	t.Setenv("CONTEXT_DROP_HERDR_SESSION", "cdx")
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != "herdr" || cfg.HerdrSession != "cdx" {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.HerdrPath != "" {
		t.Fatalf("HerdrPath should be empty when optional Herdr is unavailable: %q", cfg.HerdrPath)
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig should allow an optional Herdr installation: %v", err)
	}
}

func TestConfigureAgentPreservesArgvAndRequiresReplace(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	agent := AgentConfig{Command: []string{"/bin/echo", "--model", "a b", "@{prompt_file}"}, PromptMode: "arg"}
	if err := ConfigureAgent("custom", agent, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agents["custom"].Command; len(got) != 4 || got[2] != "a b" || got[3] != "@{prompt_file}" {
		t.Fatalf("argv = %#v", got)
	}
	if err := ConfigureAgent("custom", agent, false); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	if err := ConfigureAgent("custom", agent, true); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureAgentValidatesPromptPlaceholder(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"/bin/echo"}, {"/bin/echo", "{prompt_file}", "{prompt_file}"}} {
		if err := ConfigureAgent("bad", AgentConfig{Command: command, PromptMode: "arg"}, false); err == nil {
			t.Fatalf("expected placeholder error for %#v", command)
		}
	}
}

func TestConfigureAgentHandlesNilAgentsMap(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	nodePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, configPath, _, err := Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A previously valid config with no agents property must not panic.
	config, err := json.Marshal(map[string]any{"host": "127.0.0.1", "port": 47762, "nodePath": nodePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureAgent("cmd", AgentConfig{Command: []string{"/bin/echo", "{prompt_file}"}, PromptMode: "arg"}, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Agents["cmd"]; !ok {
		t.Fatalf("agent not persisted: %#v", cfg.Agents)
	}
}

func TestInitializeRejectsInvalidBackendEnv(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	t.Setenv("CONTEXT_DROP_BACKEND", "screen")
	if _, err := Initialize(); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadConfigRejectsInvalidBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	_, configPath, _, _ := Paths()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultBackend = "screen"
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected invalid backend error")
	}
}

func TestLoadConfigRejectsNonLoopback(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	_, configPath, _, _ := Paths()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"host":"0.0.0.0","port":47762}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected loopback error")
	}
}
