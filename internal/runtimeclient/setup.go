package runtimeclient

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type AgentConfig struct {
	Command []string `json:"command"`
}
type RuntimeConfig struct {
	Host                      string                 `json:"host"`
	Port                      int                    `json:"port"`
	StateDir                  string                 `json:"stateDir"`
	TokenFile                 string                 `json:"tokenFile"`
	NodePath                  string                 `json:"nodePath"`
	HerdrPath                 string                 `json:"herdrPath,omitempty"`
	ImsgPath                  string                 `json:"imsgPath,omitempty"`
	HerdrSession              string                 `json:"herdrSession"`
	FullAIHerdrWorkspaceLabel string                 `json:"fullAIHerdrWorkspaceLabel"`
	Agents                    map[string]AgentConfig `json:"agents"`
	// WorkerAgent selects which configured agent the four pool workers run.
	// The runtime reads it at start, so a change applies on daemon restart.
	WorkerAgent           string `json:"workerAgent"`
	ReportCredentialsFile string `json:"reportCredentialsFile"`
	// ContextDropPath is the daemon's own binary, put first on each worker's
	// PATH so `context-drop report` matches the running runtime.
	ContextDropPath string            `json:"contextDropPath"`
	RepoAliases     map[string]string `json:"repoAliases,omitempty"`
	// DelegateAgent is the pre-workerAgent name of the same setting; read only for migration.
	DelegateAgent string `json:"delegateAgent,omitempty"`
}

// WorkerAgents lists the agents the pool can run, in default-preference order.
var WorkerAgents = []string{"codex", "claude", "pi"}

// defaultAgentCommand is the unattended launch argv for a worker agent. Every
// worker runs inside a Herdr tab under the pool's control, so each agent is
// started with its own bypass-approvals flag.
func defaultAgentCommand(name, path, dari string) []string {
	flag := map[string]string{"codex": "--yolo", "claude": "--dangerously-skip-permissions", "pi": "--approve"}[name]
	if dari != "" {
		return []string{dari, "--" + name, flag}
	}
	return []string{path, flag}
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
	lock, err := lockConfig(configPath + ".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
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
	dari, _ := exec.LookPath("dari")
	for _, name := range WorkerAgents {
		if path, err := exec.LookPath(name); err == nil {
			agents[name] = AgentConfig{Command: defaultAgentCommand(name, path, dari)}
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
	herdrSession := "default"
	if value := os.Getenv("CONTEXT_DROP_HERDR_SESSION"); value != "" {
		herdrSession = value
	}
	fullAIHerdrWorkspaceLabel := "ContextDropManaged"
	if value := os.Getenv("CONTEXT_DROP_FULL_AI_HERDR_WORKSPACE_LABEL"); value != "" {
		fullAIHerdrWorkspaceLabel = value
	}
	herdrPath, _ := ResolveExecutable("herdr")
	imsgPath, _ := ResolveExecutable("imsg")
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(self); resolveErr == nil {
		self = resolved
	}
	cfg := RuntimeConfig{Host: "127.0.0.1", Port: port, StateDir: dir, TokenFile: tokenPath, NodePath: nodePath, HerdrPath: herdrPath, ImsgPath: imsgPath, HerdrSession: herdrSession, FullAIHerdrWorkspaceLabel: fullAIHerdrWorkspaceLabel, Agents: agents, ReportCredentialsFile: filepath.Join(filepath.Dir(dir), "managed", "report-credentials.json"), ContextDropPath: self, RepoAliases: map[string]string{}}
	if hasExisting {
		if existing.Host == "127.0.0.1" || existing.Host == "::1" {
			cfg.Host = existing.Host
		}
		if os.Getenv("CONTEXT_DROP_RUNTIME_PORT") == "" && existing.Port > 0 && existing.Port < 65536 {
			cfg.Port = existing.Port
		}
		if validExecutable(existing.HerdrPath) == nil {
			cfg.HerdrPath = existing.HerdrPath
		}
		if validExecutable(existing.ImsgPath) == nil {
			cfg.ImsgPath = existing.ImsgPath
		}
		if os.Getenv("CONTEXT_DROP_HERDR_SESSION") == "" && existing.HerdrSession != "" {
			cfg.HerdrSession = existing.HerdrSession
		}
		if os.Getenv("CONTEXT_DROP_FULL_AI_HERDR_WORKSPACE_LABEL") == "" && existing.FullAIHerdrWorkspaceLabel != "" {
			cfg.FullAIHerdrWorkspaceLabel = existing.FullAIHerdrWorkspaceLabel
		}
		cfg.WorkerAgent = existing.WorkerAgent
		if cfg.WorkerAgent == "" {
			cfg.WorkerAgent = existing.DelegateAgent
		}
		for alias, repo := range existing.RepoAliases {
			cfg.RepoAliases[alias] = repo
		}
		for k, v := range existing.Agents {
			// Prompt-file argv came from the retired headless launch mode; the
			// interactive default replaces it. Anything else is a user override.
			if promptFileArgv(v.Command) {
				continue
			}
			cfg.Agents[k] = v
		}
	}
	if value := os.Getenv("CONTEXT_DROP_WORKER_AGENT"); value != "" {
		cfg.WorkerAgent = value
	}
	if cfg.WorkerAgent == "" {
		for _, name := range WorkerAgents {
			if _, ok := cfg.Agents[name]; ok {
				cfg.WorkerAgent = name
				break
			}
		}
	}
	if cfg.WorkerAgent != "" {
		if _, ok := cfg.Agents[cfg.WorkerAgent]; !ok {
			return nil, fmt.Errorf("workerAgent %q is not configured", cfg.WorkerAgent)
		}
	}
	if err := writeRuntimeConfig(configPath, cfg); err != nil {
		return nil, err
	}
	return detected, nil
}

func promptFileArgv(command []string) bool {
	for _, arg := range command {
		if strings.Contains(arg, "{prompt_file}") {
			return true
		}
	}
	return false
}

// SetWorkerAgent persists the pool's agent choice. The running daemon keeps
// its current workers; the change applies on the next daemon restart.
func SetWorkerAgent(name string) error {
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
	if _, ok := cfg.Agents[name]; !ok {
		return fmt.Errorf("agent %q is not configured; available: %s", name, strings.Join(ConfiguredAgents(cfg), ", "))
	}
	cfg.WorkerAgent = name
	cfg.DelegateAgent = ""
	return writeRuntimeConfig(configPath, cfg)
}

// ConfiguredAgents lists configured agent names in WorkerAgents order, then any extras.
func ConfiguredAgents(cfg RuntimeConfig) []string {
	names := []string{}
	for _, name := range WorkerAgents {
		if _, ok := cfg.Agents[name]; ok {
			names = append(names, name)
		}
	}
	extras := []string{}
	for name := range cfg.Agents {
		if !slices.Contains(WorkerAgents, name) {
			extras = append(extras, name)
		}
	}
	slices.Sort(extras)
	return append(names, extras...)
}

func writeRuntimeConfig(configPath string, cfg RuntimeConfig) error {
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
	if err = os.Rename(tmpPath, configPath); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(configPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func ConfigureAgent(name string, agent AgentConfig, replace bool) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n/") {
		return fmt.Errorf("agent name must be a non-empty identifier without whitespace or slashes")
	}
	if len(agent.Command) == 0 {
		return fmt.Errorf("agent command must be a non-empty argv array")
	}
	for _, arg := range agent.Command {
		if arg == "" {
			return fmt.Errorf("agent command arguments must not be empty")
		}
	}
	if promptFileArgv(agent.Command) {
		return fmt.Errorf("agent command must be an interactive launch; workers receive prompts through Herdr, not a {prompt_file}")
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
	return writeRuntimeConfig(configPath, cfg)
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
	// Herdr may be absent at load time (for example on a machine that only
	// uploads); a worker launch reports the missing executable when it is
	// actually requested.
	if cfg.HerdrPath != "" {
		if err := validExecutable(cfg.HerdrPath); err != nil {
			return RuntimeConfig{}, fmt.Errorf("runtime herdrPath: %w; run context-drop init again", err)
		}
	}
	if cfg.ImsgPath != "" {
		if err := validExecutable(cfg.ImsgPath); err != nil {
			return RuntimeConfig{}, fmt.Errorf("runtime imsgPath: %w; run context-drop init again", err)
		}
	}
	if err := validExecutable(cfg.NodePath); err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtime nodePath: %w; run context-drop init again", err)
	}
	for alias, repo := range cfg.RepoAliases {
		if strings.TrimSpace(alias) == "" || strings.ContainsAny(alias, " \t\r\n/") {
			return RuntimeConfig{}, fmt.Errorf("runtime repo alias %q must be a non-empty identifier", alias)
		}
		if !filepath.IsAbs(repo) {
			return RuntimeConfig{}, fmt.Errorf("runtime repo alias %q must reference an absolute path", alias)
		}
		info, err := os.Stat(repo)
		if err != nil || !info.IsDir() {
			return RuntimeConfig{}, fmt.Errorf("runtime repo alias %q must reference an existing directory", alias)
		}
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
