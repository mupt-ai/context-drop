package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/config"
)

func TestPairingClientValidationErrors(t *testing.T) {
	if _, err := CreateChain(context.Background(), CreateChainRequest{}); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("CreateChain(no endpoint) error = %v", err)
	}
	if _, err := CreateInvite(context.Background(), CreateInviteRequest{Endpoint: "http://example.test"}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("CreateInvite(no session) error = %v", err)
	}
	if _, err := JoinChain(context.Background(), JoinChainRequest{Endpoint: "http://example.test"}); err == nil || !strings.Contains(err.Error(), "join token") {
		t.Fatalf("JoinChain(no token) error = %v", err)
	}
	if _, err := ListMachines(context.Background(), "http://example.test", ""); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("ListMachines(no session) error = %v", err)
	}
	if _, err := SendMessage(context.Background(), "http://example.test", "", "machine", "hello"); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("SendMessage(no session) error = %v", err)
	}
	if _, err := ListMessages(context.Background(), "http://example.test", ""); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("ListMessages(no session) error = %v", err)
	}
}

func TestCommandLoadConfigErrors(t *testing.T) {
	t.Setenv("CONTEXT_DROP_CONFIG", t.TempDir())
	commands := [][]string{
		{"init"},
		{"logout"},
		{"token", "create"},
		{"join", "join-token"},
		{"machines", "list"},
		{"send", "--to", "machine", "hello"},
		{"messages", "list"},
		{"list"},
		{"pull"},
		{"doctor"},
		{"config", "get"},
		{"config", "set", "machine_name", "laptop"},
	}
	for _, args := range commands {
		if _, _, err := executeRoot(t, args...); err == nil {
			t.Fatalf("executeRoot(%v) error = nil, want config load error", args)
		}
	}
}

func TestPairingCommandEndpointErrorBranches(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		switch r.URL.Path {
		case "/v1/chains":
			_, _ = w.Write([]byte(`{"error":"chain bad"}`))
		case "/v1/invites":
			_, _ = w.Write([]byte(`{"error":"invite bad"}`))
		case "/v1/join":
			_, _ = w.Write([]byte(`{"error":"join bad"}`))
		case "/v1/machines":
			_, _ = w.Write([]byte(`{"error":"machines bad"}`))
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"error":"messages bad"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: "https://wrong.example", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
	}{
		{args: []string{"init", "--endpoint", server.URL}, want: "chain bad"},
		{args: []string{"token", "create", "--endpoint", server.URL, "--ttl", "1m"}, want: "invite bad"},
		{args: []string{"join", "join-token", "--endpoint", server.URL}, want: "join bad"},
		{args: []string{"machines", "list", "--endpoint", server.URL}, want: "machines bad"},
		{args: []string{"send", "--endpoint", server.URL, "--to", "mach-2", "hello"}, want: "messages bad"},
		{args: []string{"messages", "list", "--endpoint", server.URL}, want: "messages bad"},
	}
	for _, tc := range cases {
		if _, _, err := executeRoot(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("executeRoot(%v) error = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestMachineAndMessageJSONAndSendErrors(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/machines":
			_, _ = w.Write([]byte(`{"machines":[{"id":"mach-1","chain_id":"chain-1","name":"laptop","created_at":"2026-05-23T12:00:00Z","last_seen_at":"2026-05-23T12:01:00Z"}]}`))
		case "/v1/messages":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"message":{"id":"msg-1","chain_id":"chain-1","from_machine_id":"mach-1","to_machine_id":"mach-2","body":"hello","created_at":"2026-05-23T12:02:00Z"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"msg-1","chain_id":"chain-1","from_machine_id":"mach-2","to_machine_id":"mach-1","body":"hi\nthere","created_at":"2026-05-23T12:03:00Z"}]}`))
		case "/v1/drops":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"upload failed"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeRoot(t, "machines", "list", "--json")
	if err != nil || !strings.Contains(stdout, "mach-1") {
		t.Fatalf("machines json stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = executeRoot(t, "messages", "list", "--json")
	if err != nil || !strings.Contains(stdout, "msg-1") {
		t.Fatalf("messages json stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = executeRoot(t, "send", "--to", "mach-2", "hello")
	if err != nil || !strings.Contains(stdout, "sent msg-1") {
		t.Fatalf("send text stdout=%q err=%v", stdout, err)
	}
	if _, _, err := executeRoot(t, "send", "hello"); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("send missing --to error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, "send", "--to", "mach-2", file); err == nil || !strings.Contains(err.Error(), "upload failed") {
		t.Fatalf("send file upload error = %v", err)
	}
}

func TestMachineNameFallbacks(t *testing.T) {
	if got := effectiveMachineName(config.CLIConfig{}, " override "); got != "override" {
		t.Fatalf("effectiveMachineName(override) = %q", got)
	}
	if got := effectiveMachineName(config.CLIConfig{MachineName: " saved "}, ""); got != "saved" {
		t.Fatalf("effectiveMachineName(saved) = %q", got)
	}
	if got := effectiveMachineName(config.CLIConfig{}, ""); strings.TrimSpace(got) == "" {
		t.Fatalf("effectiveMachineName(hostname) = %q", got)
	}
}

func TestSendBodyUploadsSingleFile(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/drops" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"drop-1","url":"http://example.test/d/drop-1","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`))
	}))
	defer server.Close()
	cmd := NewRootCommand(BuildInfo{})
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"send"})
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, uploaded, err := sendBody(cmd, config.CLIConfig{Endpoint: server.URL, ChainSessionToken: "session", DefaultTTL: time.Hour}, []string{file})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.ID != "drop-1" || !strings.Contains(body, "drop drop-1 http://example.test/d/drop-1 note.txt") {
		t.Fatalf("sendBody() body=%q uploaded=%+v", body, uploaded)
	}

	body, uploaded, err = sendBody(cmd, config.CLIConfig{}, []string{"hello", "there"})
	if err != nil || body != "hello there" || uploaded.ID != "" {
		t.Fatalf("sendBody(text) = %q, %+v, %v", body, uploaded, err)
	}
}
