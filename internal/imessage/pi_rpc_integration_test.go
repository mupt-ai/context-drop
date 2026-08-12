package imessage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test exercises the real installed Pi RPC process against a local
// unauthenticated protocol fixture and a copy of a supplied persistent session.
// It never sends an external provider request or mutates the source session.
func TestPiRPCRealSessionLocalProvider(t *testing.T) {
	sourceSession := os.Getenv("CONTEXT_DROP_PI_RPC_SESSION_FIXTURE")
	if sourceSession == "" {
		t.Skip("set CONTEXT_DROP_PI_RPC_SESSION_FIXTURE to run the real-session integration test")
	}
	piPath := os.Getenv("CONTEXT_DROP_PI_PATH")
	if piPath == "" {
		piPath = "/opt/homebrew/bin/pi"
	}
	if _, err := os.Stat(piPath); err != nil {
		t.Skipf("Pi executable unavailable: %v", err)
	}

	var payloadBytes, providerMessages, contentChars int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, request)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		payloadBytes = len(body)
		var payload struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			return
		}
		providerMessages = len(payload.Messages)
		for _, message := range payload.Messages {
			switch content := message.Content.(type) {
			case string:
				contentChars += len(content)
			default:
				encoded, _ := json.Marshal(content)
				contentChars += len(encoded)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"local-response\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-fast\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"local-response\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-fast\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"LOCAL_PIPELINE_OK\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"local-response\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-fast\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	models := fmt.Sprintf(`{"providers":{"local-latency":{"baseUrl":%q,"api":"openai-completions","apiKey":"local-placeholder","compat":{"supportsDeveloperRole":false,"supportsReasoningEffort":false},"models":[{"id":"fake-fast","reasoning":false,"input":["text"],"contextWindow":200000,"maxTokens":1000,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionData, err := os.ReadFile(sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	sessionCopy := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sessionCopy, sessionData, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := os.Getenv("CONTEXT_DROP_PI_RPC_CWD")
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := Defaults()
	cfg.Trusted = true
	cfg.ResponderCwd = cwd
	cfg.MemoryFile = "/durable/MEMORY.md"
	cfg.ConversationArchiveFile = "/durable/chat_full.jsonl"
	cfg.ResponderCommand = []string{piPath, "--print", "--offline", "--session", sessionCopy, "--model", "local-latency/fake-fast", "@{prompt_file}"}
	responder, ok, err := NewPiRPCResponder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("real Pi persistent responder was not recognized")
	}
	defer responder.Close()
	adapter := Adapter{Config: cfg, PersistentResponder: responder}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := adapter.RespondMeasured(ctx, Message{ID: "local-fixture", Text: "Reply with exactly LOCAL_PIPELINE_OK"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.Reply) != "LOCAL_PIPELINE_OK" {
		t.Fatalf("reply = %q", response.Reply)
	}
	if payloadBytes > 60_000 || providerMessages > 6 || contentChars > 50_000 {
		t.Fatalf("provider working context was not compacted: bytes=%d messages=%d content_chars=%d", payloadBytes, providerMessages, contentChars)
	}
	if len(response.Metrics.ModelRounds) != 1 {
		t.Fatalf("model rounds = %#v", response.Metrics.ModelRounds)
	}
	t.Logf("startup=%s responder=%s first_output=%s payload_bytes=%d provider_messages=%d content_chars=%d", response.Metrics.ResponderStartup, response.Metrics.Responder, response.Metrics.TimeToFirstOutput, payloadBytes, providerMessages, contentChars)
}
