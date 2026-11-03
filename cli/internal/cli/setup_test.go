package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"contextdrop.dev/context-drop/internal/config"
	"github.com/spf13/cobra"
)

// runWizard runs setup with scripted answers (one per line) and returns the saved config file.
func runWizard(t *testing.T, env map[string]string, modelErr error, answers ...string) (string, string) {
	t.Helper()
	path := useTempCLIConfig(t)
	oldLook, oldGet, oldRun, oldTest := lookPath, getenv, runOutput, testModel
	t.Cleanup(func() { lookPath, getenv, runOutput, testModel = oldLook, oldGet, oldRun, oldTest })
	lookPath = func(name string) (string, error) {
		switch name {
		case "tmux", "herdr", "rex", "dari":
			return "/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	getenv = func(k string) string { return env[k] }
	runOutput = func(name string, args ...string) ([]byte, error) {
		return []byte("NAME ID SIZE\nqwen3.8:27b-mlx abc 18GB\n"), nil
	}
	var tested []config.SummarizeConfig
	testModel = func(_ context.Context, sc config.SummarizeConfig) error {
		tested = append(tested, sc)
		return modelErr
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(strings.Join(answers, "\n") + "\n"))
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	if err := runSetup(cmd, newPrompter(cmd)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), out.String()
}

func TestSetupLocalModelAndRexThroughDari(t *testing.T) {
	saved, out := runWizard(t, nil, nil,
		"",  // set up the summary model
		"1", // ollama
		"",  // the first installed model
		"",  // url
		"",  // test it
		"",  // set up the launcher
		"4", // Rex block (options: whichever, tmux, herdr, rex)
		"2", // through dari
		"n", // skip uploads
	)
	for _, want := range []string{
		`summarize_provider = "ollama"`, `summarize_model = "qwen3.8:27b-mlx"`, `summarize_base_url = "http://localhost:11434"`,
		`launch_in = "rex"`, `launch_command = "dari {agent}"`,
	} {
		if !strings.Contains(saved, want) {
			t.Errorf("config missing %s:\n%s", want, saved)
		}
	}
	if !strings.Contains(out, "✓ Testing ollama qwen3.8:27b-mlx") {
		t.Errorf("output = %s", out)
	}
}

func TestSetupSavesKeyVariableNameNeverTheKey(t *testing.T) {
	env := map[string]string{
		"ANTHROPIC_AUTH_TOKEN": "secret-value", "ANTHROPIC_BASE_URL": "https://gw.example/v1x",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "dari/routing",
	}
	saved, out := runWizard(t, env, errors.New("401"),
		"",               // set up the summary model
		"2",              // anthropic
		"",               // model from ANTHROPIC_DEFAULT_SONNET_MODEL
		"",               // base URL from ANTHROPIC_BASE_URL
		"sk-live secret", // a key pasted where the variable name goes is refused
		"",               // the detected variable name
		"",               // test it (fails)
		"n",              // keep the settings anyway
		"n",              // skip the launcher
		"n",              // skip uploads
	)
	for _, want := range []string{`summarize_provider = "anthropic"`, `summarize_model = "dari/routing"`,
		`summarize_base_url = "https://gw.example/v1x"`, `summarize_api_key_env = "ANTHROPIC_AUTH_TOKEN"`} {
		if !strings.Contains(saved, want) {
			t.Errorf("config missing %s:\n%s", want, saved)
		}
	}
	if strings.Contains(saved, "secret") || strings.Contains(saved, "launch_") {
		t.Fatalf("config holds a key or launcher settings:\n%s", saved)
	}
	if !strings.Contains(out, "not the key itself") || !strings.Contains(out, "401") {
		t.Errorf("output = %s", out)
	}
}

func TestSetupCancelSavesNothing(t *testing.T) {
	path := useTempCLIConfig(t)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("\n2\n")) // input ends mid-way, as when the user quits
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	if err := runSetup(cmd, newPrompter(cmd)); !errors.Is(err, errAborted) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config was written: %v", err)
	}
}

func TestSetupWithAllDefaultsIsNotOfferedAgain(t *testing.T) {
	saved, _ := runWizard(t, nil, nil,
		"n", // skip the summary model
		"",  // set up the launcher
		"1", // whichever I'm running in
		"1", // run agents directly
		"n", // skip uploads
	)
	if !strings.Contains(saved, "setup_done = true") {
		t.Fatalf("config = %s", saved)
	}
	cfg, err := config.LoadCLIConfigFile()
	if err != nil || !cfg.Configured() {
		t.Fatalf("configured = %v, %v", cfg.Configured(), err)
	}
}

func TestMuxInstalledHonoursBinOverride(t *testing.T) {
	oldLook, oldGet := lookPath, getenv
	t.Cleanup(func() { lookPath, getenv = oldLook, oldGet })
	lookPath = func(name string) (string, error) {
		if name == "/opt/tmux-x" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	getenv = func(string) string { return "" }
	if muxInstalled("tmux") {
		t.Fatal("tmux found without it on PATH")
	}
	getenv = func(k string) string { return map[string]string{"CONTEXT_DROP_TMUX_BIN": "/opt/tmux-x"}[k] }
	if !muxInstalled("tmux") {
		t.Fatal("CONTEXT_DROP_TMUX_BIN was ignored")
	}
}

func TestSetupUploadsToMainServerWithPastedToken(t *testing.T) {
	oldCheck := checkEndpoint
	t.Cleanup(func() { checkEndpoint = oldCheck })
	checkEndpoint = func(endpoint string) error {
		if endpoint != mainServer {
			t.Errorf("checked %s", endpoint)
		}
		return nil
	}
	saved, out := runWizard(t, nil, nil,
		"n",          // skip the summary model
		"n",          // skip the launcher
		"y",          // set up uploads
		"1",          // the main server
		"main-token", // pasted token
	)
	for _, want := range []string{`endpoint = "https://contextdrop.dev"`, `upload_token = "main-token"`} {
		if !strings.Contains(saved, want) {
			t.Errorf("config missing %s:\n%s", want, saved)
		}
	}
	if !strings.Contains(out, "Checking https://contextdrop.dev") {
		t.Errorf("output = %s", out)
	}
}

func TestSetupGeneratesTokenForSelfHostedServer(t *testing.T) {
	oldCheck, oldGen := checkEndpoint, newUploadToken
	t.Cleanup(func() { checkEndpoint, newUploadToken = oldCheck, oldGen })
	checkEndpoint = func(string) error { return errors.New("connection refused") }
	newUploadToken = func() (string, error) { return "generated-123", nil }
	saved, out := runWizard(t, nil, nil,
		"n",
		"n",
		"y",
		"2",                          // a server I run
		"https://files.example.com/", // trailing slash is trimmed
		"y",                          // generate a token
	)
	for _, want := range []string{`endpoint = "https://files.example.com"`, `upload_token = "generated-123"`} {
		if !strings.Contains(saved, want) {
			t.Errorf("config missing %s:\n%s", want, saved)
		}
	}
	if !strings.Contains(out, "CONTEXT_DROP_UPLOAD_TOKEN=generated-123") || !strings.Contains(out, "saved anyway") {
		t.Errorf("output = %s", out)
	}
}
