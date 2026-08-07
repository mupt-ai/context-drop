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
	Endpoint          string
	ChainID           string
	MachineID         string
	MachineName       string
	ChainSessionToken string
	DefaultTTL        time.Duration
	Clipboard         bool
}

func DefaultCLIConfig() CLIConfig {
	return CLIConfig{
		Endpoint:   "https://contextdrop.dev",
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
	if v := os.Getenv("CONTEXT_DROP_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("parse CONTEXT_DROP_TTL: %w", err)
		}
		cfg.DefaultTTL = d
	}
	if v := os.Getenv("CONTEXT_DROP_CHAIN_ID"); v != "" {
		cfg.ChainID = v
	}
	if v := os.Getenv("CONTEXT_DROP_MACHINE_ID"); v != "" {
		cfg.MachineID = v
	}
	if v := os.Getenv("CONTEXT_DROP_MACHINE_NAME"); v != "" {
		cfg.MachineName = v
	}
	if v := os.Getenv("CONTEXT_DROP_CHAIN_SESSION_TOKEN"); v != "" {
		cfg.ChainSessionToken = v
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
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "endpoint":
			cfg.Endpoint = value
		case "chain_id":
			cfg.ChainID = value
		case "machine_id":
			cfg.MachineID = value
		case "machine_name":
			cfg.MachineName = value
		case "chain_session_token":
			cfg.ChainSessionToken = value
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
		"endpoint = %q\nchain_id = %q\nmachine_id = %q\nmachine_name = %q\nchain_session_token = %q\ndefault_ttl = %q\nclipboard = %t\n",
		cfg.Endpoint,
		cfg.ChainID,
		cfg.MachineID,
		cfg.MachineName,
		cfg.ChainSessionToken,
		cfg.DefaultTTL.String(),
		cfg.Clipboard,
	)
	return os.WriteFile(path, []byte(content), 0o600)
}

func withoutRuntimeEnvOverrides(path string, cfg CLIConfig) (CLIConfig, error) {
	persisted := DefaultCLIConfig()
	if err := loadCLIConfigFile(path, &persisted); err != nil {
		return cfg, err
	}
	cfg.Endpoint = preserveEnvString(cfg.Endpoint, persisted.Endpoint, "CONTEXT_DROP_ENDPOINT")
	cfg.DefaultTTL = preserveEnvDuration(cfg.DefaultTTL, persisted.DefaultTTL, "CONTEXT_DROP_TTL")
	cfg.ChainID = preserveEnvString(cfg.ChainID, persisted.ChainID, "CONTEXT_DROP_CHAIN_ID")
	cfg.MachineID = preserveEnvString(cfg.MachineID, persisted.MachineID, "CONTEXT_DROP_MACHINE_ID")
	cfg.MachineName = preserveEnvString(cfg.MachineName, persisted.MachineName, "CONTEXT_DROP_MACHINE_NAME")
	cfg.ChainSessionToken = preserveEnvString(
		cfg.ChainSessionToken,
		persisted.ChainSessionToken,
		"CONTEXT_DROP_CHAIN_SESSION_TOKEN",
	)
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
	Addr         string
	BaseURL      string
	Storage      string
	DataDir      string
	GCSBucket    string
	GCSPrefix    string
	JoinTokenTTL time.Duration
	DefaultTTL   time.Duration
	MaxTTL       time.Duration
	MaxBytes     int64
}

func LoadServerConfig() (ServerConfig, error) {
	cfg := ServerConfig{
		Addr:      envString("CONTEXT_DROP_ADDR", ":8080"),
		BaseURL:   strings.TrimRight(envString("CONTEXT_DROP_BASE_URL", "http://localhost:8080"), "/"),
		Storage:   envString("CONTEXT_DROP_STORAGE", "local"),
		DataDir:   envString("CONTEXT_DROP_DATA_DIR", ".data"),
		GCSBucket: os.Getenv("CONTEXT_DROP_GCS_BUCKET"),
		GCSPrefix: strings.Trim(os.Getenv("CONTEXT_DROP_GCS_PREFIX"), "/"),
		MaxBytes:  envInt64("CONTEXT_DROP_MAX_BYTES", 25*1024*1024),
	}
	var err error
	cfg.DefaultTTL, err = envDuration("CONTEXT_DROP_DEFAULT_TTL", 24*time.Hour)
	if err != nil {
		return cfg, err
	}
	cfg.JoinTokenTTL, err = envDuration("CONTEXT_DROP_JOIN_TOKEN_TTL", 10*time.Minute)
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
