package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

func TestPublicCommandSurface(t *testing.T) {
	cmd := NewRootCommand(BuildInfo{})
	var names []string
	for _, child := range cmd.Commands() {
		names = append(names, child.Name())
	}
	sort.Strings(names)
	want := []string{"daemon", "report", "schedule", "upload", "version"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("commands = %v, want %v", names, want)
	}
}

func TestRootShowsHelpAndUploadIsExplicit(t *testing.T) {
	useTempCLIConfig(t)
	stdout, _, err := executeRoot(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Available Commands") || !strings.Contains(stdout, "upload") {
		t.Fatalf("root help = %q", stdout)
	}
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, file); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("root shorthand error = %v", err)
	}
}

func TestUploadAndVersionCommands(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/drops" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop-1","url":"http://example.test/d/drop-1","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, UploadToken: "secret", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeRoot(t, "upload", file)
	if err != nil || !strings.Contains(stdout, "http://example.test/d/drop-1") {
		t.Fatalf("upload = %q, %v", stdout, err)
	}
	stdout, _, err = executeRoot(t, "version")
	if err != nil || !strings.Contains(stdout, "context-drop test") {
		t.Fatalf("version = %q, %v", stdout, err)
	}
}

func TestUploadRequiresToken(t *testing.T) {
	useTempCLIConfig(t)
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeRoot(t, "upload", file)
	if err == nil || !strings.Contains(err.Error(), "upload token is required") {
		t.Fatalf("error = %v", err)
	}
}
