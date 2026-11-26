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
	// LaunchIn is the default multiplexer for `context-drop start` (tmux, herdr, rex, or desktop).
	LaunchIn string
	// LaunchCommand starts agents instead of their bare executable; "{agent}"
	// becomes the agent kind, e.g. "dari {agent}".
	LaunchCommand string
	// SetupDone records that `context-drop setup` finished, even if every
	// answer kept a default, so it is not offered again.
	SetupDone bool
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
		"CONTEXT_DROP_LAUNCH_IN":      &cfg.LaunchIn,
		"CONTEXT_DROP_LAUNCH_COMMAND": &cfg.LaunchCommand,
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
		case "launch_in":
			cfg.LaunchIn = value
		case "launch_command":
			cfg.LaunchCommand = value
		case "setup_done":
			cfg.SetupDone = value == "true"
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
		{"launch_in", cfg.LaunchIn},
		{"launch_command", cfg.LaunchCommand},
	} {
		if kv[1] != "" {
			content += fmt.Sprintf("%s = %q\n", kv[0], kv[1])
		}
	}
	if cfg.SetupDone {
		content += "setup_done = true\n"
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
	// UploadsPerHour and UploadBytesPerHour cap anonymous uploads per client IP; 0 disables.
	UploadsPerHour     int
	UploadBytesPerHour int64
	// ProxyHops is the number of trusted proxies appending to X-Forwarded-For.
	ProxyHops int
	// AdminToken guards org creation; empty disables it.
	AdminToken string
	// OrgState is the file holding orgs and joined machines.
	OrgState string
	// Deno is the Deno executable mods run in; empty looks it up on PATH. Without Deno, mods are off.
	Deno string
	// ModsDir holds deployed mods, their key-value data, and secrets.
	ModsDir string
	// TokenKey seals the tokens extensions store, such as Slack bot tokens and Linear API keys.
	// Without it, Slack and Linear are off.
	TokenKey string
	// Linear OAuth app credentials; without a client ID, orgs connect Linear with an API key instead.
	LinearClientID      string
	LinearClientSecret  string
	LinearWebhookSecret string
	// Slack app credentials; without a client ID, Slack is off.
	SlackClientID      string
	SlackClientSecret  string
	SlackSigningSecret string
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
		AdminToken:  strings.TrimSpace(os.Getenv("CONTEXT_DROP_ADMIN_TOKEN")),
		OrgState:    strings.TrimSpace(os.Getenv("CONTEXT_DROP_ORG_STATE")),
		Deno:        strings.TrimSpace(os.Getenv("CONTEXT_DROP_DENO")),
		ModsDir:     strings.TrimSpace(os.Getenv("CONTEXT_DROP_MODS_DIR")),
		MaxBytes:    envInt64("CONTEXT_DROP_MAX_BYTES", 25*1024*1024),

		LinearClientID:      strings.TrimSpace(os.Getenv("CONTEXT_DROP_LINEAR_CLIENT_ID")),
		LinearClientSecret:  strings.TrimSpace(os.Getenv("CONTEXT_DROP_LINEAR_CLIENT_SECRET")),
		LinearWebhookSecret: strings.TrimSpace(os.Getenv("CONTEXT_DROP_LINEAR_WEBHOOK_SECRET")),
		SlackClientID:       strings.TrimSpace(os.Getenv("CONTEXT_DROP_SLACK_CLIENT_ID")),
		SlackClientSecret:   strings.TrimSpace(os.Getenv("CONTEXT_DROP_SLACK_CLIENT_SECRET")),
		SlackSigningSecret:  strings.TrimSpace(os.Getenv("CONTEXT_DROP_SLACK_SIGNING_SECRET")),
		// CONTEXT_DROP_SLACK_KEY is the token key's older name.
		TokenKey: strings.TrimSpace(firstEnv("CONTEXT_DROP_TOKEN_KEY", "CONTEXT_DROP_SLACK_KEY")),

		UploadsPerHour:     int(envInt64("CONTEXT_DROP_UPLOADS_PER_HOUR", 60)),
		UploadBytesPerHour: envInt64("CONTEXT_DROP_UPLOAD_BYTES_PER_HOUR", 250*1024*1024),
	}
	var err error
	if v := strings.TrimSpace(os.Getenv("CONTEXT_DROP_PROXY_HOPS")); v != "" {
		if cfg.ProxyHops, err = strconv.Atoi(v); err != nil || cfg.ProxyHops < 0 {
			return cfg, fmt.Errorf("CONTEXT_DROP_PROXY_HOPS must be a whole number of proxies, got %q", v)
		}
	}
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

// LoadCLIConfigFile reads only the config file, without environment
// overrides, so it can be edited and saved back.
func LoadCLIConfigFile() (CLIConfig, error) {
	cfg := DefaultCLIConfig()
	path, err := CLIConfigPath()
	if err != nil {
		return cfg, err
	}
	return cfg, loadCLIConfigFile(path, &cfg)
}

// Configured reports whether setup has run or the launcher has been set up.
func (c CLIConfig) Configured() bool {
	return c.SetupDone || c.LaunchIn != "" || c.LaunchCommand != ""
}

// firstEnv is the first of the named environment variables that is set.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
