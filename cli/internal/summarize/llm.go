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
)

// NewModel builds the model the user configured. Nothing is inferred: the
// provider must be named, and every credential or endpoint must be named too,
// so a stray environment variable never sends transcripts somewhere unexpected.
//
//   - anthropic: model and api_key_env are required; base_url defaults to api.anthropic.com
//   - openai: model is required; base_url defaults to api.openai.com/v1; api_key_env is
//     required for that default endpoint and optional for other OpenAI-compatible servers
//   - ollama: model is required; base_url defaults to http://localhost:11434
//   - command: command is required
func NewModel(cfg config.SummarizeConfig, getenv func(string) string, client *http.Client) (Model, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		return nil, fmt.Errorf("no summarize provider configured; set summarize_provider in ~/.context-drop/config.toml, CONTEXT_DROP_SUMMARIZE_PROVIDER, or --provider (anthropic, openai, ollama, or command)")
	}
	switch provider {
	case ProviderAnthropic:
		if cfg.Model == "" {
			return nil, fmt.Errorf("anthropic provider needs summarize_model (or --model)")
		}
		if cfg.APIKeyEnv == "" {
			return nil, fmt.Errorf("anthropic provider needs summarize_api_key_env (or --api-key-env), the name of the variable holding your key")
		}
		key := getenv(cfg.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("$%s is empty; it must hold the API key for the anthropic provider", cfg.APIKeyEnv)
		}
		base := firstNonEmpty(cfg.BaseURL, "https://api.anthropic.com")
		return &anthropicModel{client: client, base: strings.TrimRight(base, "/"), model: cfg.Model, apiKey: key}, nil
	case ProviderOpenAI:
		if cfg.Model == "" {
			return nil, fmt.Errorf("openai provider needs summarize_model (or --model)")
		}
		base := firstNonEmpty(cfg.BaseURL, "https://api.openai.com/v1")
		key := ""
		if cfg.APIKeyEnv != "" {
			key = getenv(cfg.APIKeyEnv)
			if key == "" {
				return nil, fmt.Errorf("$%s is empty; it must hold the API key for the openai provider", cfg.APIKeyEnv)
			}
		} else if cfg.BaseURL == "" {
			return nil, fmt.Errorf("openai provider with the default endpoint needs summarize_api_key_env (or --api-key-env)")
		}
		return &openAIModel{client: client, base: strings.TrimRight(base, "/"), model: cfg.Model, apiKey: key, provider: provider}, nil
	case ProviderOllama:
		if cfg.Model == "" {
			return nil, fmt.Errorf("ollama provider needs summarize_model (or --model), e.g. gpt-oss:20b")
		}
		base := firstNonEmpty(cfg.BaseURL, "http://localhost:11434")
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		if !strings.HasSuffix(strings.TrimRight(base, "/"), "/v1") {
			base = strings.TrimRight(base, "/") + "/v1"
		}
		return &openAIModel{client: client, base: strings.TrimRight(base, "/"), model: cfg.Model, provider: provider}, nil
	case ProviderCommand:
		if strings.TrimSpace(cfg.Command) == "" {
			return nil, fmt.Errorf("command provider needs summarize_command (or --command)")
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
}

func (m *anthropicModel) Name() string { return "anthropic/" + m.model }

func (m *anthropicModel) Complete(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{
		"model":      m.model,
		"max_tokens": 2048,
		"system":     system,
		"messages":   []map[string]any{{"role": "user", "content": prompt}},
	}
	headers := map[string]string{
		"anthropic-version": "2023-06-01",
		"x-api-key":         m.apiKey,
		"Authorization":     "Bearer " + m.apiKey,
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
