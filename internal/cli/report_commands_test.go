package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
