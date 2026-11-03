package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/localhome"
)

const AppName = "context-drop"

type CLIConfig struct {
	Endpoint    string
	UploadToken string
	DefaultTTL  time.Duration
	Clipboard   bool
	Summarize   SummarizeConfig
	// LaunchIn is the default multiplexer for `context-drop start` (tmux, herdr, or rex).
	LaunchIn string
	// LaunchCommand starts agents instead of their bare executable; "{agent}"
	// becomes the agent kind, e.g. "dari {agent}".
	LaunchCommand string
}

// SummarizeConfig selects the model `context-drop summarize` uses.
type SummarizeConfig struct {
	// Provider is anthropic, openai, ollama, or command; empty picks one from the environment.
	Provider string
	Model    string
	BaseURL  string
	// APIKeyEnv names the environment variable holding the API key.
	APIKeyEnv string
	// Command is a shell command that reads the prompt on stdin (provider "command").
	Command string
}

func DefaultCLIConfig() CLIConfig {
	return CLIConfig{
		DefaultTTL: 24 * time.Hour,
		Clipboard:  false,
	}
}

func CLIConfigPath() (string, error) {
	if v := os.Getenv("CONTEXT_DROP_CONFIG"); v != "" {
		return v, nil
	}
	root, err := localhome.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "config.toml"), nil
}

func LoadCLIConfig() (CLIConfig, error) {
	cfg := DefaultCLIConfig()
	path, err := CLIConfigPath()
	if err != nil {
		return cfg, err
	}
	if err := loadCLIConfigFile(path, &cfg); err != nil {
		return cfg, err
	}

	if v := os.Getenv("CONTEXT_DROP_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("CONTEXT_DROP_UPLOAD_TOKEN"); v != "" {
		cfg.UploadToken = v
	}
	for key, dst := range map[string]*string{
		"CONTEXT_DROP_SUMMARIZE_PROVIDER":    &cfg.Summarize.Provider,
		"CONTEXT_DROP_SUMMARIZE_MODEL":       &cfg.Summarize.Model,
		"CONTEXT_DROP_SUMMARIZE_BASE_URL":    &cfg.Summarize.BaseURL,
		"CONTEXT_DROP_SUMMARIZE_API_KEY_ENV": &cfg.Summarize.APIKeyEnv,
		"CONTEXT_DROP_SUMMARIZE_COMMAND":     &cfg.Summarize.Command,
		"CONTEXT_DROP_LAUNCH_IN":             &cfg.LaunchIn,
		"CONTEXT_DROP_LAUNCH_COMMAND":        &cfg.LaunchCommand,
	} {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	if v := os.Getenv("CONTEXT_DROP_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("parse CONTEXT_DROP_TTL: %w", err)
		}
		cfg.DefaultTTL = d
	}
	return cfg, nil
}

func loadCLIConfigFile(path string, cfg *CLIConfig) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid config line %q", line)
		}
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))
		switch key {
		case "endpoint":
			cfg.Endpoint = value
		case "upload_token":
			cfg.UploadToken = value
		case "default_ttl":
			d, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("parse default_ttl: %w", err)
			}
			cfg.DefaultTTL = d
		case "clipboard":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("parse clipboard: %w", err)
			}
			cfg.Clipboard = b
		case "summarize_provider":
			cfg.Summarize.Provider = value
		case "summarize_model":
			cfg.Summarize.Model = value
		case "summarize_base_url":
			cfg.Summarize.BaseURL = value
		case "summarize_api_key_env":
			cfg.Summarize.APIKeyEnv = value
		case "summarize_command":
			cfg.Summarize.Command = value
		case "launch_in":
			cfg.LaunchIn = value
		case "launch_command":
			cfg.LaunchCommand = value
		}
	}
	return scanner.Err()
}

func SaveCLIConfig(cfg CLIConfig) error {
	path, err := CLIConfigPath()
	if err != nil {
		return err
	}
	cfg, err = withoutRuntimeEnvOverrides(path, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(
		"endpoint = %q\nupload_token = %q\ndefault_ttl = %q\nclipboard = %t\n",
		cfg.Endpoint,
		cfg.UploadToken,
		cfg.DefaultTTL.String(),
		cfg.Clipboard,
	)
	for _, kv := range [][2]string{
		{"summarize_provider", cfg.Summarize.Provider},
		{"summarize_model", cfg.Summarize.Model},
		{"summarize_base_url", cfg.Summarize.BaseURL},
		{"summarize_api_key_env", cfg.Summarize.APIKeyEnv},
		{"summarize_command", cfg.Summarize.Command},
		{"launch_in", cfg.LaunchIn},
		{"launch_command", cfg.LaunchCommand},
	} {
		if kv[1] != "" {
			content += fmt.Sprintf("%s = %q\n", kv[0], kv[1])
		}
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func withoutRuntimeEnvOverrides(path string, cfg CLIConfig) (CLIConfig, error) {
	persisted := DefaultCLIConfig()
	if err := loadCLIConfigFile(path, &persisted); err != nil {
		return cfg, err
	}
	cfg.Endpoint = preserveEnvString(cfg.Endpoint, persisted.Endpoint, "CONTEXT_DROP_ENDPOINT")
	cfg.UploadToken = preserveEnvString(cfg.UploadToken, persisted.UploadToken, "CONTEXT_DROP_UPLOAD_TOKEN")
	cfg.DefaultTTL = preserveEnvDuration(cfg.DefaultTTL, persisted.DefaultTTL, "CONTEXT_DROP_TTL")
	return cfg, nil
}

func preserveEnvString(value, persisted, key string) string {
	if env := os.Getenv(key); env != "" && value == env {
		return persisted
	}
	return value
}

func preserveEnvDuration(value, persisted time.Duration, key string) time.Duration {
	env := os.Getenv(key)
	if env == "" {
		return value
	}
	parsed, err := time.ParseDuration(env)
	if err == nil && value == parsed {
		return persisted
	}
	return value
}

type ServerConfig struct {
	Addr        string
	BaseURL     string
	Storage     string
	DataDir     string
	GCSBucket   string
	GCSPrefix   string
	UploadToken string
	DefaultTTL  time.Duration
	MaxTTL      time.Duration
	MaxBytes    int64
}

func LoadServerConfig() (ServerConfig, error) {
	cfg := ServerConfig{
		Addr:        envString("CONTEXT_DROP_ADDR", ":8080"),
		BaseURL:     strings.TrimRight(envString("CONTEXT_DROP_BASE_URL", "http://localhost:8080"), "/"),
		Storage:     envString("CONTEXT_DROP_STORAGE", "local"),
		DataDir:     envString("CONTEXT_DROP_DATA_DIR", ".data"),
		GCSBucket:   os.Getenv("CONTEXT_DROP_GCS_BUCKET"),
		GCSPrefix:   strings.Trim(os.Getenv("CONTEXT_DROP_GCS_PREFIX"), "/"),
		UploadToken: strings.TrimSpace(os.Getenv("CONTEXT_DROP_UPLOAD_TOKEN")),
		MaxBytes:    envInt64("CONTEXT_DROP_MAX_BYTES", 25*1024*1024),
	}
	if cfg.UploadToken == "" {
		return cfg, fmt.Errorf("CONTEXT_DROP_UPLOAD_TOKEN is required")
	}
	var err error
	cfg.DefaultTTL, err = envDuration("CONTEXT_DROP_DEFAULT_TTL", 24*time.Hour)
	if err != nil {
		return cfg, err
	}
	cfg.MaxTTL, err = envDuration("CONTEXT_DROP_MAX_TTL", 7*24*time.Hour)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", key, err)
		}
		return d, nil
	}
	return fallback, nil
}

// unquote accepts TOML-style basic ("...") and literal ('...') strings and bare values.
func unquote(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if v, err := strconv.Unquote(value); err == nil {
			return v
		}
	}
	return strings.Trim(value, `"'`)
}
