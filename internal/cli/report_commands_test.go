package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReportCommandUsesScopedWorkerEnvironment(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-cap" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	t.Setenv("CONTEXT_DROP_REPORT_URL", server.URL)
	t.Setenv("CONTEXT_DROP_REPORT_CAPABILITY", "scoped-cap")
	t.Setenv("CONTEXT_DROP_RUN_ID", "run-private")
	cmd := newReportCommand()
	cmd.SetArgs(nil)
	cmd.SetIn(strings.NewReader("natural worker update\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got["runId"] != "run-private" || got["message"] != "natural worker update" {
		t.Fatalf("payload = %#v", got)
	}
	if _, ok := got["kind"]; ok {
		t.Fatalf("report must not send a typed kind: %#v", got)
	}
}

func TestReportCommandFallsBackToCredentialsFile(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer file-cap" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	root := t.TempDir()
	t.Setenv("CONTEXT_DROP_HOME", root)
	t.Setenv("HERDR_PANE_ID", "w9:p4")
	credsDir := root + "/managed"
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := map[string]map[string]string{"w9:p4": {"url": server.URL, "capability": "file-cap", "runId": "run-from-file"}}
	data, _ := json.Marshal(creds)
	if err := os.WriteFile(credsDir+"/report-credentials.json", data, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newReportCommand()
	cmd.SetArgs([]string{"file-based report"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got["runId"] != "run-from-file" || got["message"] != "file-based report" {
		t.Fatalf("payload = %#v", got)
	}
}
