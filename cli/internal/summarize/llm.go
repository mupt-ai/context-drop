// Package summarize turns live agent transcripts into short status summaries
// with a model of the user's choice, re-summarizing only sessions that changed.
package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/config"
)

// Model completes one prompt.
type Model interface {
	Name() string
	Complete(ctx context.Context, system, prompt string) (string, error)
}

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderOllama    = "ollama"
	ProviderCommand   = "command"

	defaultAnthropicModel = "claude-sonnet-5-5"
	defaultOllamaModel    = "llama3.2"
)

// NewModel builds the configured model. With no provider configured it uses
// Anthropic when an Anthropic credential is present, OpenAI when an OpenAI key
// is present, and otherwise a local Ollama server.
func NewModel(cfg config.SummarizeConfig, getenv func(string) string, client *http.Client) (Model, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		switch {
		case cfg.Command != "":
			provider = ProviderCommand
		case getenv("ANTHROPIC_API_KEY") != "" || getenv("ANTHROPIC_AUTH_TOKEN") != "":
			provider = ProviderAnthropic
		case getenv("OPENAI_API_KEY") != "":
			provider = ProviderOpenAI
		default:
			provider = ProviderOllama
		}
	}
	switch provider {
	case ProviderAnthropic:
		base := firstNonEmpty(cfg.BaseURL, getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com")
		keyEnv := firstNonEmpty(cfg.APIKeyEnv, "ANTHROPIC_API_KEY")
		m := &anthropicModel{client: client, base: strings.TrimRight(base, "/"), model: firstNonEmpty(cfg.Model, getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"), defaultAnthropicModel), apiKey: getenv(keyEnv)}
		if cfg.APIKeyEnv == "" {
			// Gateways configured for Claude Code authenticate with a bearer token.
			m.bearer = getenv("ANTHROPIC_AUTH_TOKEN")
		}
		if m.apiKey == "" && m.bearer == "" {
			return nil, fmt.Errorf("anthropic provider needs an API key in $%s", keyEnv)
		}
		return m, nil
	case ProviderOpenAI, ProviderOllama:
		base, model, keyEnv := cfg.BaseURL, cfg.Model, cfg.APIKeyEnv
		if provider == ProviderOllama {
			base = firstNonEmpty(base, getenv("OLLAMA_HOST"), "http://localhost:11434")
			if !strings.HasSuffix(strings.TrimRight(base, "/"), "/v1") {
				base = strings.TrimRight(base, "/") + "/v1"
			}
			if !strings.Contains(base, "://") {
				base = "http://" + base
			}
			model = firstNonEmpty(model, defaultOllamaModel)
		} else {
			base = firstNonEmpty(base, getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
			keyEnv = firstNonEmpty(keyEnv, "OPENAI_API_KEY")
			if model == "" {
				return nil, fmt.Errorf("openai provider needs --model (or summarize_model in config)")
			}
		}
		key := ""
		if keyEnv != "" {
			key = getenv(keyEnv)
		}
		return &openAIModel{client: client, base: strings.TrimRight(base, "/"), model: model, apiKey: key, provider: provider}, nil
	case ProviderCommand:
		if strings.TrimSpace(cfg.Command) == "" {
			return nil, fmt.Errorf("command provider needs --command (or summarize_command in config)")
		}
		return &commandModel{command: cfg.Command}, nil
	}
	return nil, fmt.Errorf("unknown summarize provider %q (want anthropic, openai, ollama, or command)", cfg.Provider)
}

type anthropicModel struct {
	client *http.Client
	base   string
	model  string
	apiKey string
	bearer string
}

func (m *anthropicModel) Name() string { return "anthropic/" + m.model }

func (m *anthropicModel) Complete(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{
		"model":      m.model,
		"max_tokens": 2048,
		"system":     system,
		"messages":   []map[string]any{{"role": "user", "content": prompt}},
	}
	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if m.apiKey != "" {
		headers["x-api-key"] = m.apiKey
	}
	if m.bearer != "" {
		headers["Authorization"] = "Bearer " + m.bearer
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := postJSON(ctx, m.client, m.base+"/v1/messages", headers, body, &resp); err != nil {
		return "", err
	}
	var parts []string
	for _, c := range resp.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "")), nil
}

type openAIModel struct {
	client   *http.Client
	base     string
	model    string
	apiKey   string
	provider string
}

func (m *openAIModel) Name() string { return m.provider + "/" + m.model }

func (m *openAIModel) Complete(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{
		"model": m.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
	}
	headers := map[string]string{}
	if m.apiKey != "" {
		headers["Authorization"] = "Bearer " + m.apiKey
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := postJSON(ctx, m.client, m.base+"/chat/completions", headers, body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%s returned no choices", m.Name())
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

type commandModel struct{ command string }

func (m *commandModel) Name() string { return "command: " + m.command }

func (m *commandModel) Complete(ctx context.Context, system, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", m.command)
	cmd.Stdin = strings.NewReader(system + "\n\n" + prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("summarize command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("POST %s: %s: %s", url, resp.Status, truncate(strings.TrimSpace(string(raw)), 500))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("POST %s: decode response: %w", url, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
