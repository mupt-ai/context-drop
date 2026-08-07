package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/config"
)

func executeRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(BuildInfo{Version: "test", Commit: "abc", Date: "2026-05-23"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func useTempCLIConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("CONTEXT_DROP_CONFIG", path)
	return path
}

func TestRootUploadListPullAndLinkPassthrough(t *testing.T) {
	useTempCLIConfig(t)
	var uploaded bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/v1/drops":
			if r.Method == http.MethodPost {
				uploaded = true
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprint(w, `{"id":"drop-1","url":"http://example.test/d/drop-1","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"drops":[{"id":"drop-1","url":"http://example.test/d/drop-1","filename":"hello.txt","content_type":"text/plain","size":5,"created_at":"2026-05-23T11:00:00Z","expires_at":"2026-05-23T12:00:00Z"}]}`)
		case "/v1/drops/drop-1/blob":
			w.Header().Set("Content-Disposition", `attachment; filename="hello.txt"`)
			_, _ = fmt.Fprint(w, "hello")
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeRoot(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if !uploaded || !strings.Contains(stdout, "http://example.test/d/drop-1") {
		t.Fatalf("upload stdout = %q uploaded=%v", stdout, uploaded)
	}

	stdout, _, err = executeRoot(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "drop-1") || !strings.Contains(stdout, "hello.txt") {
		t.Fatalf("list stdout = %q", stdout)
	}

	out := filepath.Join(t.TempDir(), "pulled.txt")
	stdout, _, err = executeRoot(t, "pull", "drop-1", "--output", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, out) {
		t.Fatalf("pull stdout = %q", stdout)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "hello" {
		t.Fatalf("pulled data = %q, %v", data, err)
	}

	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeRoot(t, "https://example.test/already-there")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "https://example.test/already-there") || !strings.Contains(stderr, "not initialized") {
		t.Fatalf("link stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRootUploadRequiresChain(t *testing.T) {
	useTempCLIConfig(t)
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeRoot(t, file)
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("upload error = %v, want not initialized", err)
	}
}

func TestInitJoinTokenMachinesMessagesAndSendFileCommands(t *testing.T) {
	useTempCLIConfig(t)
	var sentBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chains":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"chain_id":"chain-1","machine_id":"mach-1","machine_name":"laptop","session_token":"session-1"}`)
		case "/v1/invites":
			if got := r.Header.Get("Authorization"); got != "Bearer session-1" {
				t.Fatalf("invite Authorization = %q", got)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"token":"join-token","expires_at":"2026-05-23T12:10:00Z","chain_id":"chain-1","machine_id":"mach-1","machine_name":"laptop","session_token":"session-1b"}`)
		case "/v1/join":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"chain_id":"chain-1","machine_id":"mach-2","machine_name":"desktop","session_token":"session-2"}`)
		case "/v1/machines":
			_, _ = fmt.Fprint(w, `{"machines":[{"id":"mach-1","chain_id":"chain-1","name":"laptop","created_at":"2026-05-23T12:00:00Z","last_seen_at":"2026-05-23T12:01:00Z"}]}`)
		case "/v1/drops":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"drop-1","url":"http://example.test/d/drop-1","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
		case "/v1/messages":
			if r.Method == http.MethodPost {
				var body struct {
					Body string `json:"body"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				sentBody = body.Body
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprint(w, `{"message":{"id":"msg-1","chain_id":"chain-1","from_machine_id":"mach-1","to_machine_id":"mach-2","body":"ok","created_at":"2026-05-23T12:02:00Z"}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"messages":[{"id":"msg-1","chain_id":"chain-1","from_machine_id":"mach-2","to_machine_id":"mach-1","body":"hi","created_at":"2026-05-23T12:03:00Z"}]}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	stdout, _, err := executeRoot(t, "init", "--endpoint", server.URL, "--machine-name", "laptop")
	if err != nil || !strings.Contains(stdout, "initialized chain chain-1") {
		t.Fatalf("init stdout=%q err=%v", stdout, err)
	}
	cfg, err := config.LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChainSessionToken != "session-1" || cfg.MachineID != "mach-1" {
		t.Fatalf("config after init = %+v", cfg)
	}

	stdout, stderr, err := executeRoot(t, "token", "create")
	if err != nil || !strings.Contains(stdout, "join token: join-token") || !strings.Contains(stderr, "expires") {
		t.Fatalf("token stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, _, err = executeRoot(t, "join", "join-token", "--endpoint", server.URL, "--machine-name", "desktop")
	if err != nil || !strings.Contains(stdout, "joined chain chain-1") {
		t.Fatalf("join stdout=%q err=%v", stdout, err)
	}

	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, ChainID: "chain-1", MachineID: "mach-1", MachineName: "laptop", ChainSessionToken: "session-1", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = executeRoot(t, "machines", "list")
	if err != nil || !strings.Contains(stdout, "mach-1\tlaptop") {
		t.Fatalf("machines stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = executeRoot(t, "messages", "list")
	if err != nil || !strings.Contains(stdout, "mach-2\thi") {
		t.Fatalf("messages stdout=%q err=%v", stdout, err)
	}

	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = executeRoot(t, "send", "--to", "desktop", file)
	if err != nil || !strings.Contains(stdout, "sent msg-1") || !strings.Contains(stdout, "http://example.test/d/drop-1") {
		t.Fatalf("send file stdout=%q err=%v", stdout, err)
	}
	if !strings.Contains(sentBody, "drop drop-1 http://example.test/d/drop-1 note.txt") {
		t.Fatalf("sent body = %q", sentBody)
	}
}

func TestConfigDoctorLogoutAndVersion(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok\n")
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, ChainSessionToken: "secret-session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := executeRoot(t, "config", "get")
	if err != nil || !strings.Contains(stdout, "secret...sion") || strings.Contains(stdout, "secret-session") {
		t.Fatalf("config get stdout=%q err=%v", stdout, err)
	}
	stdout, stderr, err := executeRoot(t, "doctor")
	if err != nil || !strings.Contains(stdout, "ok") || !strings.Contains(stderr, "chain: configured") {
		t.Fatalf("doctor stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, _, err = executeRoot(t, "logout")
	if err != nil || !strings.Contains(stdout, "logged out") {
		t.Fatalf("logout stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = executeRoot(t, "version")
	if err != nil || !strings.Contains(stdout, "context-drop test") {
		t.Fatalf("version stdout=%q err=%v", stdout, err)
	}
}
