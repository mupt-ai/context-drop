package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useTempConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("CONTEXT_DROP_CONFIG", path)
	return path
}

func TestCLIConfigPathDefault(t *testing.T) {
	t.Setenv("CONTEXT_DROP_CONFIG", "")
	path, err := CLIConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join(AppName, "config.toml")) {
		t.Fatalf("CLIConfigPath() = %q", path)
	}
}

func TestLoadCLIConfigDefaults(t *testing.T) {
	path := useTempConfig(t)
	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://contextdrop.dev" || cfg.DefaultTTL != 24*time.Hour || cfg.Clipboard {
		t.Fatalf("LoadCLIConfig defaults from %s = %+v", path, cfg)
	}
}

func TestLoadCLIConfigFileAndEnvOverrides(t *testing.T) {
	path := useTempConfig(t)
	content := strings.Join([]string{
		``,
		`# comment`,
		`endpoint = "https://file.example"`,
		`upload_token = "upload-file"`,
		`chain_id = "chain-file"`,
		`machine_id = "mach-file"`,
		`machine_name = "file-machine"`,
		`chain_session_token = "session-file"`,
		`default_ttl = "1h"`,
		`clipboard = true`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CONTEXT_DROP_ENDPOINT", "https://env.example")
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "upload-env")
	t.Setenv("CONTEXT_DROP_CHAIN_ID", "chain-env")
	t.Setenv("CONTEXT_DROP_MACHINE_ID", "mach-env")
	t.Setenv("CONTEXT_DROP_MACHINE_NAME", "env-machine")
	t.Setenv("CONTEXT_DROP_CHAIN_SESSION_TOKEN", "session-env")
	t.Setenv("CONTEXT_DROP_TTL", "30m")

	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://env.example" || cfg.UploadToken != "upload-env" || cfg.ChainID != "chain-env" || cfg.MachineID != "mach-env" || cfg.MachineName != "env-machine" || cfg.ChainSessionToken != "session-env" || cfg.DefaultTTL != 30*time.Minute || !cfg.Clipboard {
		t.Fatalf("LoadCLIConfig() = %+v", cfg)
	}
}

func TestLoadCLIConfigEnvDurationError(t *testing.T) {
	useTempConfig(t)
	t.Setenv("CONTEXT_DROP_TTL", "bad")
	if _, err := LoadCLIConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_TTL") {
		t.Fatalf("LoadCLIConfig(env ttl) error = %v, want env ttl error", err)
	}
}

func TestLoadCLIConfigValidationErrors(t *testing.T) {
	path := useTempConfig(t)
	if err := os.WriteFile(path, []byte("default_ttl = \"bad\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLIConfig(); err == nil || !strings.Contains(err.Error(), "parse default_ttl") {
		t.Fatalf("LoadCLIConfig(default_ttl) error = %v, want parse default_ttl", err)
	}

	if err := os.WriteFile(path, []byte("clipboard = \"maybe\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLIConfig(); err == nil || !strings.Contains(err.Error(), "parse clipboard") {
		t.Fatalf("LoadCLIConfig(clipboard) error = %v, want parse clipboard", err)
	}

	if err := os.WriteFile(path, []byte("not a config line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLIConfig(); err == nil || !strings.Contains(err.Error(), "invalid config line") {
		t.Fatalf("LoadCLIConfig(line) error = %v, want invalid config line", err)
	}
}

func TestSaveCLIConfigErrors(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXT_DROP_CONFIG", filepath.Join(parentFile, "config.toml"))
	if err := SaveCLIConfig(DefaultCLIConfig()); err == nil {
		t.Fatal("SaveCLIConfig(parent file) error = nil, want error")
	}

	if _, err := withoutRuntimeEnvOverrides(t.TempDir(), DefaultCLIConfig()); err == nil {
		t.Fatal("withoutRuntimeEnvOverrides(directory) error = nil, want error")
	}
}

func TestSaveCLIConfigPreservesRuntimeEnvOverrides(t *testing.T) {
	path := useTempConfig(t)
	persisted := CLIConfig{
		Endpoint:          "https://file.example",
		UploadToken:       "upload-file",
		ChainID:           "chain-file",
		MachineID:         "mach-file",
		MachineName:       "file-machine",
		ChainSessionToken: "session-file",
		DefaultTTL:        time.Hour,
		Clipboard:         true,
	}
	if err := SaveCLIConfig(persisted); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CONTEXT_DROP_ENDPOINT", "https://env.example")
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "upload-env")
	t.Setenv("CONTEXT_DROP_CHAIN_SESSION_TOKEN", "session-env")
	runtimeCfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg.MachineName = "updated"
	if err := SaveCLIConfig(runtimeCfg); err != nil {
		t.Fatal(err)
	}

	loaded := DefaultCLIConfig()
	if err := loadCLIConfigFile(path, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Endpoint != persisted.Endpoint || loaded.UploadToken != persisted.UploadToken || loaded.ChainSessionToken != persisted.ChainSessionToken || loaded.MachineName != "updated" {
		t.Fatalf("persisted config = %+v", loaded)
	}
}

func TestLoadServerConfig(t *testing.T) {
	t.Setenv("CONTEXT_DROP_ADDR", ":9999")
	t.Setenv("CONTEXT_DROP_BASE_URL", "https://drop.example.com/")
	t.Setenv("CONTEXT_DROP_STORAGE", "gcs")
	t.Setenv("CONTEXT_DROP_DATA_DIR", ".data-test")
	t.Setenv("CONTEXT_DROP_GCS_BUCKET", "bucket")
	t.Setenv("CONTEXT_DROP_GCS_PREFIX", "/prefix/")
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "upload-secret")
	t.Setenv("CONTEXT_DROP_DEFAULT_TTL", "2h")
	t.Setenv("CONTEXT_DROP_MAX_TTL", "24h")
	t.Setenv("CONTEXT_DROP_MAX_BYTES", "1234")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9999" || cfg.BaseURL != "https://drop.example.com" || cfg.Storage != "gcs" || cfg.DataDir != ".data-test" || cfg.GCSBucket != "bucket" || cfg.GCSPrefix != "prefix" || cfg.UploadToken != "upload-secret" || cfg.DefaultTTL != 2*time.Hour || cfg.MaxTTL != 24*time.Hour || cfg.MaxBytes != 1234 {
		t.Fatalf("LoadServerConfig() = %+v", cfg)
	}
}

func TestPreserveAndEnvHelpers(t *testing.T) {
	if got := preserveEnvDuration(time.Hour, 2*time.Hour, "MISSING_ENV"); got != time.Hour {
		t.Fatalf("preserveEnvDuration(missing) = %s", got)
	}
	t.Setenv("DURATION_ENV", "1h")
	if got := preserveEnvDuration(time.Hour, 2*time.Hour, "DURATION_ENV"); got != 2*time.Hour {
		t.Fatalf("preserveEnvDuration(match) = %s", got)
	}
	t.Setenv("DURATION_ENV", "bad")
	if got := preserveEnvDuration(time.Hour, 2*time.Hour, "DURATION_ENV"); got != time.Hour {
		t.Fatalf("preserveEnvDuration(bad) = %s", got)
	}
	if got := envString("MISSING_STRING_ENV", "fallback"); got != "fallback" {
		t.Fatalf("envString fallback = %q", got)
	}
	if got := envInt64("MISSING_INT_ENV", 42); got != 42 {
		t.Fatalf("envInt64 fallback = %d", got)
	}
	t.Setenv("BAD_INT_ENV", "bad")
	if got := envInt64("BAD_INT_ENV", 42); got != 42 {
		t.Fatalf("envInt64 bad = %d", got)
	}
	if got, err := envDuration("MISSING_DURATION_ENV", time.Minute); err != nil || got != time.Minute {
		t.Fatalf("envDuration fallback = %s, %v", got, err)
	}
}

func TestLoadServerConfigRequiresUploadToken(t *testing.T) {
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "")
	if _, err := LoadServerConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_UPLOAD_TOKEN is required") {
		t.Fatalf("LoadServerConfig() error = %v", err)
	}
}

func TestLoadServerConfigDurationError(t *testing.T) {
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "upload-secret")
	t.Setenv("CONTEXT_DROP_DEFAULT_TTL", "bad")
	if _, err := LoadServerConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_DEFAULT_TTL") {
		t.Fatalf("LoadServerConfig() error = %v, want duration error", err)
	}
	t.Setenv("CONTEXT_DROP_DEFAULT_TTL", "1h")
	t.Setenv("CONTEXT_DROP_MAX_TTL", "bad")
	if _, err := LoadServerConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_MAX_TTL") {
		t.Fatalf("LoadServerConfig() max ttl error = %v", err)
	}
}
