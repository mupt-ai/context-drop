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
	if cfg.Port != 49123 || cfg.Host != "127.0.0.1" || cfg.DefaultBackend != "tmux" || cfg.HerdrSession != "default" {
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
