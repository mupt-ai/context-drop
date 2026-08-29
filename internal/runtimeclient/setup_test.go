package runtimeclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if cfg.Port != 49123 || cfg.Host != "127.0.0.1" || cfg.DefaultBackend != "herdr" || cfg.HerdrSession != "default" || cfg.FullAIHerdrWorkspaceLabel != "ContextDropManaged" {
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

func TestInitializePreservesValidatedRepoAliases(t *testing.T) {
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
	repo := t.TempDir()
	cfg.RepoAliases = map[string]string{"context-drop": repo}
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
	loaded, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RepoAliases["context-drop"] != repo {
		t.Fatalf("repoAliases = %#v", loaded.RepoAliases)
	}
}

func TestConfigureRepoAliasAddsAndRemovesCanonicalDirectory(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := ConfigureRepoAlias("context-drop", repo, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(repo)
	if cfg.RepoAliases["context-drop"] != resolved {
		t.Fatalf("repoAliases = %#v", cfg.RepoAliases)
	}
	if err := ConfigureRepoAlias("context-drop", "", true); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.RepoAliases["context-drop"]; ok {
		t.Fatalf("alias was not removed: %#v", cfg.RepoAliases)
	}
}

func TestInitializeWritesAtomicallyAndConcurrentAliasIsPreserved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", home)
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := ConfigureRepoAlias("context-drop", repo, false); err != nil {
		t.Fatal(err)
	}
	// Re-running Initialize must not lose the alias written concurrently.
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RepoAliases["context-drop"] == "" {
		t.Fatalf("repo alias was lost after Initialize: %#v", cfg.RepoAliases)
	}
	// Verify no leftover temp files in the config directory.
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestConfigureRepoAliasRejectsInvalidInput(t *testing.T) {

	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"bad alias": t.TempDir(), "relative": "relative/path", "missing": filepath.Join(t.TempDir(), "missing")} {
		if err := ConfigureRepoAlias(name, path, false); err == nil {
			t.Fatalf("ConfigureRepoAlias(%q, %q) succeeded", name, path)
		}
	}
}

func TestLoadConfigRejectsInvalidRepoAliases(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	_, configPath, _, _ := Paths()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for name, aliases := range map[string]map[string]string{
		"invalid name":  {"bad alias": t.TempDir()},
		"relative path": {"repo": "relative/path"},
		"missing path":  {"repo": filepath.Join(t.TempDir(), "missing")},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			candidate.RepoAliases = aliases
			data, marshalErr := json.Marshal(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := os.WriteFile(configPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(); err == nil {
				t.Fatal("expected invalid repo alias error")
			}
		})
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
	if got := cfg.Agents["pi"].Command; len(got) != 3 || got[1] != "--approve" || got[2] != "@{prompt_file}" {
		t.Fatalf("Pi command = %#v", got)
	}
}

func TestInitializeMigratesPiCommandWithoutApprove(t *testing.T) {
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
	cfg.Agents["pi"] = AgentConfig{Command: []string{"/opt/homebrew/bin/pi", "@{prompt_file}"}, PromptMode: "arg"}
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
	if got := cfg.Agents["pi"].Command; len(got) != 3 || got[1] != "--approve" || got[2] != "@{prompt_file}" {
		t.Fatalf("Pi command = %#v", got)
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
	t.Setenv("CONTEXT_DROP_FULL_AI_HERDR_WORKSPACE_LABEL", "ManagedAI")
	if _, err := Initialize(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != "herdr" || cfg.HerdrSession != "cdx" || cfg.FullAIHerdrWorkspaceLabel != "ManagedAI" {
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

func TestLaunchManagedScheduleCallsManagedEndpointWithOwner(t *testing.T) {
	var seenPath, seenMethod string
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenPath, seenMethod = req.URL.Path, req.Method
		_ = json.NewDecoder(req.Body).Decode(&seenBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runId":"run_1","task":{"paneId":"w1:p2","agent":"pi","name":"schedule-x","status":"running","selected":false,"fullyManaged":true}}`))
	}))
	defer server.Close()
	client := &Client{Address: server.URL, Token: "secret", HTTP: server.Client()}
	task, err := client.LaunchManagedSchedule(context.Background(), "pi", "/repo", "check", "schedule-x", "herdr", "scheduler", "chat-a")
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/v1/tasks/schedule" || seenMethod != http.MethodPost {
		t.Fatalf("unexpected call: %s %s", seenMethod, seenPath)
	}
	if seenBody["routerId"] != "scheduler" || seenBody["chatId"] != "chat-a" || seenBody["backend"] != "herdr" || seenBody["agent"] != "pi" {
		t.Fatalf("unexpected body: %#v", seenBody)
	}
	if task.RunID != "run_1" || task.PaneID != "w1:p2" || !task.FullyManaged || task.Status != "running" {
		t.Fatalf("unexpected task: %#v", task)
	}
}
