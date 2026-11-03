package summarize

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/agents"
	"contextdrop.dev/context-drop/internal/config"
)

type recordingModel struct {
	prompts []string
}

func (m *recordingModel) Name() string { return "fake/model" }

func (m *recordingModel) Complete(_ context.Context, system, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	if strings.HasPrefix(system, "You brief") {
		return "- overview", nil
	}
	return "summary " + string(rune('A'+len(m.prompts)-1)), nil
}

func session(id string) agents.Session {
	return agents.Session{ID: id, Agent: agents.Claude, NativeID: id, Status: agents.StatusIdle, Cwd: "/work"}
}

func msg(role, text string, minute int) agents.Message {
	return agents.Message{Role: role, Text: text, Time: time.Date(2026, 11, 3, 5, minute, 0, 0, time.UTC)}
}

func TestRunOnlyResummarizesChangedSessions(t *testing.T) {
	model := &recordingModel{}
	s := &Summarizer{Model: model, StatePath: filepath.Join(t.TempDir(), "summaries.json"), Concurrency: 1}
	first := []Input{
		{Session: session("cc-a"), Messages: []agents.Message{msg("user", "build the parser", 1), msg("assistant", "Parser done.", 2)}},
		{Session: session("cc-b"), Messages: []agents.Message{msg("user", "write docs", 1)}},
	}
	report, err := s.Run(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) != 2 || !report.Sessions[0].Changed || !report.Sessions[1].Changed {
		t.Fatalf("first run: prompts=%d report=%+v", len(model.prompts), report.Sessions)
	}

	// Nothing changed: no model calls, previous summaries reused.
	report, err = s.Run(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) != 2 || report.Sessions[0].Changed || report.Sessions[0].Summary != first0(report) {
		t.Fatalf("unchanged run: prompts=%d report=%+v", len(model.prompts), report.Sessions)
	}

	// cc-a gets new messages: only it is re-summarized, from the previous summary plus the new messages.
	changed := []Input{
		{Session: session("cc-a"), Messages: append(first[0].Messages, msg("user", "now add tests", 3), msg("assistant", "Tests fail on Windows.", 4))},
		first[1],
	}
	report, err = s.Run(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) != 3 || !report.Sessions[0].Changed || report.Sessions[1].Changed {
		t.Fatalf("changed run: prompts=%d report=%+v", len(model.prompts), report.Sessions)
	}
	prompt := model.prompts[2]
	if !strings.Contains(prompt, "Previous summary") || !strings.Contains(prompt, "Tests fail on Windows.") || strings.Contains(prompt, "build the parser") {
		t.Fatalf("incremental prompt = %s", prompt)
	}
}

func first0(r Report) string { return r.Sessions[0].Summary }

func TestRunKeepsPreviousSummaryWhenTranscriptFails(t *testing.T) {
	model := &recordingModel{}
	s := &Summarizer{Model: model, StatePath: filepath.Join(t.TempDir(), "s.json")}
	in := Input{Session: session("cc-a"), Messages: []agents.Message{msg("user", "hi", 1)}}
	if _, err := s.Run(context.Background(), []Input{in}); err != nil {
		t.Fatal(err)
	}
	in.Messages, in.Err = nil, io.ErrUnexpectedEOF
	report, err := s.Run(context.Background(), []Input{in})
	if err != nil {
		t.Fatal(err)
	}
	got := report.Sessions[0]
	if got.Summary != "summary A" || got.Error == "" || got.Changed {
		t.Fatalf("report = %+v", got)
	}
}

func TestOverviewIsCachedUntilSummariesChange(t *testing.T) {
	model := &recordingModel{}
	s := &Summarizer{Model: model, StatePath: filepath.Join(t.TempDir(), "s.json"), Overview: true, Concurrency: 1}
	inputs := []Input{
		{Session: session("cc-a"), Messages: []agents.Message{msg("user", "x", 1)}},
		{Session: session("cc-b"), Messages: []agents.Message{msg("user", "y", 1)}},
	}
	report, _ := s.Run(context.Background(), inputs)
	if report.Overview != "- overview" || len(model.prompts) != 3 {
		t.Fatalf("overview=%q prompts=%d", report.Overview, len(model.prompts))
	}
	report, _ = s.Run(context.Background(), inputs)
	if report.Overview != "- overview" || len(model.prompts) != 3 {
		t.Fatalf("cached overview=%q prompts=%d", report.Overview, len(model.prompts))
	}
}

func TestNewModelRequiresExplicitSetup(t *testing.T) {
	noEnv := func(string) string { return "" }
	// Credentials in the environment must not be used unless named.
	withKeys := func(k string) string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-ambient", "ANTHROPIC_AUTH_TOKEN": "tok", "OPENAI_API_KEY": "sk-openai", "OLLAMA_HOST": "10.0.0.5"}[k]
	}
	refuse := func(name string, cfg config.SummarizeConfig, getenv func(string) string, want string) {
		t.Helper()
		if _, err := NewModel(cfg, getenv, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, want)
		}
	}
	refuse("no provider", config.SummarizeConfig{}, withKeys, "no summarize provider configured")
	refuse("anthropic without model", config.SummarizeConfig{Provider: "anthropic", APIKeyEnv: "ANTHROPIC_API_KEY"}, withKeys, "summarize_model")
	refuse("anthropic without key name", config.SummarizeConfig{Provider: "anthropic", Model: "m"}, withKeys, "summarize_api_key_env")
	refuse("anthropic empty key", config.SummarizeConfig{Provider: "anthropic", Model: "m", APIKeyEnv: "MISSING"}, withKeys, "$MISSING is empty")
	refuse("openai default endpoint without key name", config.SummarizeConfig{Provider: "openai", Model: "m"}, withKeys, "summarize_api_key_env")
	refuse("openai's own URL spelled out without key name", config.SummarizeConfig{Provider: "openai", Model: "m", BaseURL: "https://api.openai.com/v1/"}, withKeys, "summarize_api_key_env")
	refuse("ollama without model", config.SummarizeConfig{Provider: "ollama"}, withKeys, "summarize_model")
	refuse("unknown", config.SummarizeConfig{Provider: "nope"}, noEnv, "unknown summarize provider")

	var gotPath string
	var gotHeaders http.Header
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeaders = r.URL.Path, r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&gotBody)
		if strings.HasSuffix(r.URL.Path, "/v1/messages") {
			w.Write([]byte(`{"content":[{"type":"text","text":"from claude"}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"from openai"}}]}`))
	}))
	defer srv.Close()

	m, err := NewModel(config.SummarizeConfig{Provider: "anthropic", Model: "gw/model", APIKeyEnv: "GW_KEY", BaseURL: srv.URL},
		func(k string) string {
			if k == "GW_KEY" {
				return "gw-secret"
			}
			return withKeys(k)
		}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Complete(context.Background(), "sys", "prompt")
	if err != nil || out != "from claude" || gotPath != "/v1/messages" || gotHeaders.Get("x-api-key") != "gw-secret" ||
		gotHeaders.Get("Authorization") != "Bearer gw-secret" || gotBody["model"] != "gw/model" || gotBody["system"] != "sys" {
		t.Fatalf("anthropic: out=%q err=%v path=%s headers=%v body=%v", out, err, gotPath, gotHeaders, gotBody)
	}

	// Ollama takes a bare host:port and needs no key.
	m, err = NewModel(config.SummarizeConfig{Provider: "ollama", BaseURL: strings.TrimPrefix(srv.URL, "http://"), Model: "gpt-oss:20b"}, withKeys, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.Complete(context.Background(), "sys", "prompt")
	if err != nil || out != "from openai" || gotPath != "/v1/chat/completions" || gotHeaders.Get("Authorization") != "" || gotBody["model"] != "gpt-oss:20b" {
		t.Fatalf("ollama: out=%q err=%v path=%s headers=%v", out, err, gotPath, gotHeaders)
	}

	// An Ollama proxy that wants a key gets the named one.
	m, err = NewModel(config.SummarizeConfig{Provider: "ollama", BaseURL: srv.URL, Model: "llama", APIKeyEnv: "GW_KEY"},
		func(k string) string {
			if k == "GW_KEY" {
				return "proxy-secret"
			}
			return ""
		}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(context.Background(), "sys", "prompt"); err != nil || gotHeaders.Get("Authorization") != "Bearer proxy-secret" {
		t.Fatalf("ollama with key: err=%v headers=%v", err, gotHeaders)
	}

	// An OpenAI-compatible local server needs no key when a base URL is named.
	m, err = NewModel(config.SummarizeConfig{Provider: "openai", BaseURL: srv.URL, Model: "qwen3"}, withKeys, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(context.Background(), "sys", "prompt"); err != nil || gotHeaders.Get("Authorization") != "" {
		t.Fatalf("openai-compatible: err=%v headers=%v", err, gotHeaders)
	}
}

func TestCommandProvider(t *testing.T) {
	m, err := NewModel(config.SummarizeConfig{Provider: "command", Command: "tr a-z A-Z"}, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Complete(context.Background(), "sys", "hello")
	if err != nil || out != "SYS\n\nHELLO" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
