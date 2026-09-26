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
	useTempConfig(t)
	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://contextdrop.dev" || cfg.DefaultTTL != 24*time.Hour || cfg.Clipboard {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadCLIConfigFileAndEnvOverrides(t *testing.T) {
	path := useTempConfig(t)
	content := "endpoint = \"https://file.example\"\nupload_token = \"upload-file\"\ndefault_ttl = \"1h\"\nclipboard = true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXT_DROP_ENDPOINT", "https://env.example")
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "upload-env")
	t.Setenv("CONTEXT_DROP_TTL", "30m")
	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://env.example" || cfg.UploadToken != "upload-env" || cfg.DefaultTTL != 30*time.Minute || !cfg.Clipboard {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadCLIConfigIgnoresRemovedKeys(t *testing.T) {
	path := useTempConfig(t)
	if err := os.WriteFile(path, []byte("chain_id = \"old\"\nmachine_id = \"old\"\nchain_session_token = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLIConfig(); err != nil {
		t.Fatalf("old keys should be harmless during migration: %v", err)
	}
}

func TestLoadCLIConfigValidationErrors(t *testing.T) {
	path := useTempConfig(t)
	for content, want := range map[string]string{
		"default_ttl = \"bad\"\n": "parse default_ttl",
		"clipboard = \"maybe\"\n": "parse clipboard",
		"not a config line\n":     "invalid config line",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCLIConfig(); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("content %q error = %v", content, err)
		}
	}
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXT_DROP_TTL", "bad")
	if _, err := LoadCLIConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_TTL") {
		t.Fatalf("env ttl error = %v", err)
	}
}

func TestSaveCLIConfigPreservesRuntimeEnvOverrides(t *testing.T) {
	path := useTempConfig(t)
	persisted := CLIConfig{Endpoint: "https://file.example", UploadToken: "upload-file", DefaultTTL: time.Hour, Clipboard: true}
	if err := SaveCLIConfig(persisted); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXT_DROP_ENDPOINT", "https://env.example")
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "upload-env")
	runtimeCfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg.Clipboard = false
	if err := SaveCLIConfig(runtimeCfg); err != nil {
		t.Fatal(err)
	}
	loaded := DefaultCLIConfig()
	if err := loadCLIConfigFile(path, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Endpoint != persisted.Endpoint || loaded.UploadToken != persisted.UploadToken || loaded.Clipboard {
		t.Fatalf("persisted config = %+v", loaded)
	}
}

func TestSaveCLIConfigErrors(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXT_DROP_CONFIG", filepath.Join(parentFile, "config.toml"))
	if err := SaveCLIConfig(DefaultCLIConfig()); err == nil {
		t.Fatal("expected parent error")
	}
	if _, err := withoutRuntimeEnvOverrides(t.TempDir(), DefaultCLIConfig()); err == nil {
		t.Fatal("expected directory error")
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
		t.Fatalf("server config = %+v", cfg)
	}
}

func TestConfigHelpersAndServerErrors(t *testing.T) {
	if got := preserveEnvDuration(time.Hour, 2*time.Hour, "MISSING_ENV"); got != time.Hour {
		t.Fatalf("duration = %s", got)
	}
	t.Setenv("DURATION_ENV", "1h")
	if got := preserveEnvDuration(time.Hour, 2*time.Hour, "DURATION_ENV"); got != 2*time.Hour {
		t.Fatalf("duration = %s", got)
	}
	if got := envString("MISSING_STRING_ENV", "fallback"); got != "fallback" {
		t.Fatalf("string = %q", got)
	}
	if got := envInt64("MISSING_INT_ENV", 42); got != 42 {
		t.Fatalf("int = %d", got)
	}
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "")
	if _, err := LoadServerConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_UPLOAD_TOKEN is required") {
		t.Fatalf("missing token error = %v", err)
	}
	t.Setenv("CONTEXT_DROP_UPLOAD_TOKEN", "secret")
	t.Setenv("CONTEXT_DROP_DEFAULT_TTL", "bad")
	if _, err := LoadServerConfig(); err == nil || !strings.Contains(err.Error(), "CONTEXT_DROP_DEFAULT_TTL") {
		t.Fatalf("duration error = %v", err)
	}
}
