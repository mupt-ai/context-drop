package runtimeclient

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type AgentConfig struct {
	Command    []string `json:"command"`
	PromptMode string   `json:"promptMode"`
}
type RuntimeConfig struct {
	Host                      string                 `json:"host"`
	Port                      int                    `json:"port"`
	StateDir                  string                 `json:"stateDir"`
	TokenFile                 string                 `json:"tokenFile"`
	NodePath                  string                 `json:"nodePath"`
	DefaultBackend            string                 `json:"defaultBackend"`
	TmuxSession               string                 `json:"tmuxSession"`
	HerdrPath                 string                 `json:"herdrPath,omitempty"`
	HerdrSession              string                 `json:"herdrSession"`
	FullAIHerdrWorkspaceLabel string                 `json:"fullAIHerdrWorkspaceLabel"`
	Agents                    map[string]AgentConfig `json:"agents"`
	DelegateAgent             string                 `json:"delegateAgent,omitempty"`
}

func Initialize() ([]string, error) {
	dir, configPath, tokenPath, err := Paths()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.WriteFile(tokenPath, []byte(base64.RawURLEncoding.EncodeToString(b)+"\n"), 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		return nil, err
	}
	var existing RuntimeConfig
	hasExisting := false
	if current, readErr := os.ReadFile(configPath); readErr == nil && json.Unmarshal(current, &existing) == nil {
		hasExisting = true
	}
	nodePath := ""
	if hasExisting && validExecutable(existing.NodePath) == nil {
		nodePath = existing.NodePath
	} else {
		nodePath, err = ResolveExecutable("node")
		if err != nil {
			return nil, fmt.Errorf("Node 20+ is required for the local runtime: %w", err)
		}
	}
	agents := map[string]AgentConfig{}
	detected := []string{}
	for _, name := range []string{"pi", "codex", "claude"} {
		if path, err := exec.LookPath(name); err == nil {
			promptArg := "{prompt_file}"
			if name == "pi" {
				promptArg = "@{prompt_file}"
			}
			agents[name] = AgentConfig{Command: []string{path, promptArg}, PromptMode: "arg"}
			detected = append(detected, name)
		}
	}
	port := 47762
	if value := os.Getenv("CONTEXT_DROP_RUNTIME_PORT"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed <= 0 || parsed > 65535 {
			return nil, fmt.Errorf("CONTEXT_DROP_RUNTIME_PORT must be between 1 and 65535")
		}
		port = parsed
	}
	backend := "herdr"
	if value := os.Getenv("CONTEXT_DROP_BACKEND"); value != "" {
		if value != "tmux" && value != "herdr" {
			return nil, fmt.Errorf("CONTEXT_DROP_BACKEND must be tmux or herdr")
		}
		backend = value
	}
	herdrSession := "default"
	if value := os.Getenv("CONTEXT_DROP_HERDR_SESSION"); value != "" {
		herdrSession = value
	}
	fullAIHerdrWorkspaceLabel := "ContextDropManaged"
	if value := os.Getenv("CONTEXT_DROP_FULL_AI_HERDR_WORKSPACE_LABEL"); value != "" {
		fullAIHerdrWorkspaceLabel = value
	}
	herdrPath, _ := ResolveExecutable("herdr")
	delegateAgent := ""
	if _, ok := agents["pi"]; ok {
		delegateAgent = "pi"
	}
	cfg := RuntimeConfig{Host: "127.0.0.1", Port: port, StateDir: dir, TokenFile: tokenPath, NodePath: nodePath, DefaultBackend: backend, TmuxSession: "context-drop", HerdrPath: herdrPath, HerdrSession: herdrSession, FullAIHerdrWorkspaceLabel: fullAIHerdrWorkspaceLabel, Agents: agents, DelegateAgent: delegateAgent}
	if hasExisting {
		if existing.Host == "127.0.0.1" || existing.Host == "::1" {
			cfg.Host = existing.Host
		}
		if os.Getenv("CONTEXT_DROP_RUNTIME_PORT") == "" && existing.Port > 0 && existing.Port < 65536 {
			cfg.Port = existing.Port
		}
		if os.Getenv("CONTEXT_DROP_BACKEND") == "" && (existing.DefaultBackend == "tmux" || existing.DefaultBackend == "herdr") {
			cfg.DefaultBackend = existing.DefaultBackend
		}
		if existing.TmuxSession != "" {
			cfg.TmuxSession = existing.TmuxSession
		}
		if validExecutable(existing.HerdrPath) == nil {
			cfg.HerdrPath = existing.HerdrPath
		}
		if os.Getenv("CONTEXT_DROP_HERDR_SESSION") == "" && existing.HerdrSession != "" {
			cfg.HerdrSession = existing.HerdrSession
		}
		if os.Getenv("CONTEXT_DROP_FULL_AI_HERDR_WORKSPACE_LABEL") == "" && existing.FullAIHerdrWorkspaceLabel != "" {
			cfg.FullAIHerdrWorkspaceLabel = existing.FullAIHerdrWorkspaceLabel
		}
		if existing.DelegateAgent != "" {
			cfg.DelegateAgent = existing.DelegateAgent
		}
		for k, v := range existing.Agents {
			// Older auto-detected Pi configs passed the prompt path as plain text.
			// Pi loads file content only when the argument uses its @file syntax.
			if k == "pi" && len(v.Command) == 2 && v.Command[1] == "{prompt_file}" {
				v.Command[1] = "@{prompt_file}"
			}
			cfg.Agents[k] = v
		}
	}
	if cfg.DelegateAgent == "" {
		if _, ok := cfg.Agents["pi"]; ok {
			cfg.DelegateAgent = "pi"
		}
	}
	if cfg.DelegateAgent != "" {
		if _, ok := cfg.Agents[cfg.DelegateAgent]; !ok {
			return nil, fmt.Errorf("delegateAgent %q is not configured", cfg.DelegateAgent)
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return nil, err
	}
	return detected, nil
}

func ConfigureAgent(name string, agent AgentConfig, replace bool) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n/") {
		return fmt.Errorf("agent name must be a non-empty identifier without whitespace or slashes")
	}
	if agent.PromptMode != "arg" {
		return fmt.Errorf("promptMode must be arg")
	}
	if len(agent.Command) == 0 {
		return fmt.Errorf("agent command must be a non-empty argv array")
	}
	placeholders := 0
	for _, arg := range agent.Command {
		if arg == "" {
			return fmt.Errorf("agent command arguments must not be empty")
		}
		placeholders += strings.Count(arg, "{prompt_file}")
	}
	if placeholders != 1 {
		return fmt.Errorf("agent command must contain exactly one {prompt_file} placeholder")
	}
	_, configPath, _, err := Paths()
	if err != nil {
		return err
	}
	lock, err := lockConfig(configPath + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	if _, exists := cfg.Agents[name]; exists && !replace {
		return fmt.Errorf("agent %q is already configured; pass --replace to overwrite it", name)
	}
	cfg.Agents[name] = agent
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, configPath)
}

func LoadConfig() (RuntimeConfig, error) {
	_, configPath, _, err := Paths()
	if err != nil {
		return RuntimeConfig{}, err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return RuntimeConfig{}, err
	}
	var cfg RuntimeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RuntimeConfig{}, fmt.Errorf("read runtime config: %w", err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "::1" {
		return RuntimeConfig{}, fmt.Errorf("runtime host must be loopback")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return RuntimeConfig{}, fmt.Errorf("runtime port must be between 1 and 65535")
	}
	if cfg.DefaultBackend == "" {
		cfg.DefaultBackend = "tmux"
	}
	if cfg.DefaultBackend != "tmux" && cfg.DefaultBackend != "herdr" {
		return RuntimeConfig{}, fmt.Errorf("runtime defaultBackend must be tmux or herdr")
	}
	// Herdr is optional. Keep the runtime usable for pairing, handoffs, and
	// tmux launches when it is not installed; a Herdr launch will report the
	// missing executable when it is actually requested.
	if cfg.HerdrPath != "" {
		if err := validExecutable(cfg.HerdrPath); err != nil {
			return RuntimeConfig{}, fmt.Errorf("runtime herdrPath: %w; run context-drop init again", err)
		}
	}
	if err := validExecutable(cfg.NodePath); err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtime nodePath: %w; run context-drop init again", err)
	}
	return cfg, nil
}

// ResolveExecutable returns a canonical absolute executable path suitable for
// service-manager environments with a minimal PATH.
func ResolveExecutable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	if err := validExecutable(path); err != nil {
		return "", err
	}
	return path, nil
}

func validExecutable(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("must be an absolute executable path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

func RuntimeEntry() (string, error) {
	if v := os.Getenv("CONTEXT_DROP_RUNTIME_ENTRY"); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "lib", "context-drop", "runtime", "dist", "src", "main.js")
		if _, e := os.Stat(candidate); e == nil {
			return candidate, nil
		}
	}
	for _, candidate := range []string{"runtime/dist/src/main.js", "./runtime/dist/src/main.js"} {
		if abs, e := filepath.Abs(candidate); e == nil {
			if _, e = os.Stat(abs); e == nil {
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("runtime assets not found; run make runtime-build or reinstall context-drop")
}
