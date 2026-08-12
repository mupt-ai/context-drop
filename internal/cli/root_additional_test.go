package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/config"
	"github.com/spf13/cobra"
)

func TestUploadInputHelpers(t *testing.T) {
	if _, _, _, err := inputData(nil, false); err == nil || !strings.Contains(err.Error(), "no path") {
		t.Fatalf("inputData(no path) error = %v", err)
	}
	if _, _, _, err := inputData([]string{filepath.Join(t.TempDir(), "missing")}, false); err == nil {
		t.Fatal("inputData(missing) error = nil, want error")
	}
	if _, _, _, err := inputData(nil, true); err == nil || !strings.Contains(err.Error(), "clipboard") {
		t.Fatalf("inputData(clipboard) error = %v, want clipboard error", err)
	}
	file := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(file, []byte("png bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, name, contentType, err := inputData([]string{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "png bytes" || name != "image.png" || contentType != "image/png" {
		t.Fatalf("inputData() = %q, %q, %q", data, name, contentType)
	}
}

func TestLinkOnlyAndOutputHelpers(t *testing.T) {
	if link, ok := linkOnlyInput([]string{"https://example.test/a"}); !ok || link == "" {
		t.Fatalf("linkOnlyInput() = %q, %v", link, ok)
	}
	if _, ok := linkOnlyInput([]string{"not-a-url"}); ok {
		t.Fatal("linkOnlyInput(non-url) = true, want false")
	}
	if _, ok := linkOnlyInput([]string{"ftp://example.test/a"}); ok {
		t.Fatal("linkOnlyInput(ftp) = true, want false")
	}
	if got := outputPath("", false, "id", "file.txt"); got != filepath.Join(defaultPullDir, "file.txt") {
		t.Fatalf("outputPath() = %q", got)
	}
	if got := outputPath("", false, "id", ""); got != filepath.Join(defaultPullDir, "id") {
		t.Fatalf("outputPath(empty filename) = %q", got)
	}
	if got := outputPath("/tmp/out", false, "id", "file.txt"); got != "/tmp/out" {
		t.Fatalf("outputPath(file) = %q", got)
	}
	if got := outputPath("/tmp", true, "id", "file.txt"); got != filepath.Join("/tmp", "file.txt") {
		t.Fatalf("outputPath(dir) = %q", got)
	}
	if !isImageContentType("image/png") || isImageContentType("text/plain") {
		t.Fatal("isImageContentType mismatch")
	}
	if id, ok := firstNewImageDrop([]DropSummary{{ID: "old", ContentType: "image/png"}, {ID: "new", ContentType: "image/jpeg"}}, map[string]bool{"old": true}); !ok || id != "new" {
		t.Fatalf("firstNewImageDrop() = %q, %v", id, ok)
	}
	if _, ok := firstNewImageDrop([]DropSummary{{ID: "new", ContentType: "text/plain"}}, nil); ok {
		t.Fatal("firstNewImageDrop(text) = true, want false")
	}
}

func TestWaitForNewImageDropValidation(t *testing.T) {
	if _, err := waitForNewImageDrop(context.Background(), "https://example.test", "session", -time.Second); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("waitForNewImageDrop negative error = %v", err)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestUploadCommandJSONAndFlagBranches(t *testing.T) {
	useTempCLIConfig(t)
	var sawUpload bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/drops" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		sawUpload = true
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Filename"); got != "custom.log" {
			t.Fatalf("X-Filename = %q", got)
		}
		if got := r.Header.Get("X-TTL"); got != "30m0s" {
			t.Fatalf("X-TTL = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "text/custom" {
			t.Fatalf("Content-Type = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop-json","url":"http://example.test/d/drop-json","expires_at":"2026-05-23T12:00:00Z","content_type":"text/custom","size":5}`)
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: "https://wrong.example", UploadToken: "session", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeRoot(t, "upload", "--endpoint", server.URL, "--ttl", "30m", "--filename", "custom.log", "--content-type", "text/custom", "--json", file)
	if err != nil {
		t.Fatal(err)
	}
	if !sawUpload || !strings.Contains(stdout, "drop-json") {
		t.Fatalf("stdout=%q sawUpload=%v", stdout, sawUpload)
	}
}

func TestRunUploadErrorBranches(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":"upload bad"}`)
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, UploadToken: "session", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, file); err == nil || !strings.Contains(err.Error(), "upload bad") {
		t.Fatalf("upload server error = %v", err)
	}

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop-json","url":"http://example.test/d/drop-json","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
	}))
	defer okServer.Close()
	cmd := &cobra.Command{}
	cmd.SetOut(errorWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	if err := runUpload(context.Background(), cmd, &options{endpoint: okServer.URL, json: true}, []string{file}); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("runUpload(json writer) error = %v", err)
	}

	linkCmd := &cobra.Command{}
	linkCmd.SetOut(errorWriter{})
	linkCmd.SetErr(&bytes.Buffer{})
	if err := writeLinkOnly(linkCmd, false, true, "https://example.test/a"); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("writeLinkOnly(json writer) error = %v", err)
	}
}

func TestRunUploadAndClipboardBranches(t *testing.T) {
	useTempCLIConfig(t)
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetContext(context.Background())
	if err := runUpload(context.Background(), cmd, &options{clipboard: true, noClipboard: true}, nil); err == nil || !strings.Contains(err.Error(), "--clipboard") {
		t.Fatalf("runUpload(conflict) error = %v", err)
	}
	if err := runUpload(context.Background(), cmd, &options{json: true}, []string{"https://example.test/a"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "https://example.test/a") || !strings.Contains(stderr.String(), "not initialized") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	if got, err := effectiveClipboard(nil, config.CLIConfig{Clipboard: true}); err != nil || !got {
		t.Fatalf("effectiveClipboard(nil) = %v, %v", got, err)
	}
	if got, err := effectiveClipboard(&options{clipboard: true}, config.CLIConfig{}); err != nil || !got {
		t.Fatalf("effectiveClipboard(clipboard) = %v, %v", got, err)
	}
	if got, err := effectiveClipboard(&options{noCopy: true}, config.CLIConfig{Clipboard: true}); err != nil || got {
		t.Fatalf("effectiveClipboard(noCopy) = %v, %v", got, err)
	}

	copyURLToClipboardIfRequested(cmd, false, "https://example.test")
	copyURLToClipboardIfRequested(cmd, true, "https://example.test")
	if !strings.Contains(stderr.String(), "failed to copy") && !strings.Contains(stderr.String(), "copied URL") {
		t.Fatalf("clipboard stderr = %q", stderr.String())
	}
}

func TestMimeTypeByExtBranches(t *testing.T) {
	cases := map[string]string{
		"a.jpg":  "image/jpeg",
		"a.jpeg": "image/jpeg",
		"a.gif":  "image/gif",
		"a.webp": "image/webp",
		"a.pdf":  "application/pdf",
		"a.md":   "text/plain",
		"a.bin":  "",
	}
	for name, want := range cases {
		if got := mimeTypeByExt(name); got != want {
			t.Fatalf("mimeTypeByExt(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestListPullDoctorErrorBranches(t *testing.T) {
	useTempCLIConfig(t)
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":"server bad"}`)
	}))
	defer errorServer.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: "https://wrong.example", UploadToken: "session", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, "list", "--endpoint", errorServer.URL); err == nil || !strings.Contains(err.Error(), "server bad") {
		t.Fatalf("list endpoint error = %v", err)
	}
	if _, _, err := executeRoot(t, "pull", "--endpoint", errorServer.URL); err == nil || !strings.Contains(err.Error(), "server bad") {
		t.Fatalf("pull endpoint error = %v", err)
	}
	if _, _, err := executeRoot(t, "pull", "--watch", "drop-id"); err == nil || !strings.Contains(err.Error(), "--watch") {
		t.Fatalf("pull watch args error = %v", err)
	}

	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"drops":[]}`)
	}))
	defer emptyServer.Close()
	if _, _, err := executeRoot(t, "pull", "--endpoint", emptyServer.URL); err == nil || !strings.Contains(err.Error(), "no drops") {
		t.Fatalf("pull no drops error = %v", err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := pullDrops(cmd, errorServer.URL, "session", []string{"drop-id"}, "", false); err == nil || !strings.Contains(err.Error(), "server bad") {
		t.Fatalf("pullDrops pull error = %v", err)
	}

	blobServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "hello")
	}))
	defer blobServer.Close()
	existing := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pullDrops(cmd, blobServer.URL, "session", []string{"drop-id"}, existing, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("pullDrops existing error = %v", err)
	}
	missingParentOutput := filepath.Join(t.TempDir(), "missing", "out.txt")
	if err := pullDrops(cmd, blobServer.URL, "session", []string{"drop-id"}, missingParentOutput, true); err == nil {
		t.Fatal("pullDrops write error = nil, want error")
	}

	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: errorServer.URL, UploadToken: "session", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, "doctor"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("doctor status error = %v", err)
	}
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: "http://[::1", UploadToken: "session", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, "doctor"); err == nil {
		t.Fatal("doctor invalid endpoint error = nil, want error")
	}
}

func TestListPullWatchAndConfigBranches(t *testing.T) {
	useTempCLIConfig(t)
	var listCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = fmt.Fprint(w, "ok\n")
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/v1/drops":
			listCalls++
			_, _ = fmt.Fprint(w, `{"drops":[{"id":"one","url":"http://example.test/d/one","filename":"one.png","content_type":"image/png","size":1,"created_at":"2026-05-23T11:00:00Z","expires_at":"2026-05-23T12:00:00Z"},{"id":"two","url":"http://example.test/d/two","filename":"two.txt","content_type":"text/plain","size":1,"created_at":"2026-05-23T10:00:00Z","expires_at":"2026-05-23T12:00:00Z"}]}`)
		case "/v1/drops/one/blob":
			w.Header().Set("Content-Disposition", `attachment; filename="one.png"`)
			_, _ = fmt.Fprint(w, "1")
		case "/v1/drops/two/blob":
			w.Header().Set("Content-Disposition", `attachment; filename="two.txt"`)
			_, _ = fmt.Fprint(w, "2")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, UploadToken: "session", ChainSessionToken: "session", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := executeRoot(t, "list", "--json")
	if err != nil || !strings.Contains(stdout, "one") {
		t.Fatalf("list json stdout=%q err=%v", stdout, err)
	}
	outDir := t.TempDir()
	stdout, _, err = executeRoot(t, "pull", "one", "two", "--output", outDir, "--force")
	if err != nil || !strings.Contains(stdout, filepath.Join(outDir, "one.png")) || !strings.Contains(stdout, filepath.Join(outDir, "two.txt")) {
		t.Fatalf("pull multi stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = executeRoot(t, "pull", "--output", filepath.Join(t.TempDir(), "latest.png"), "--force")
	if err != nil || !strings.Contains(stdout, "latest.png") {
		t.Fatalf("pull latest stdout=%q err=%v", stdout, err)
	}
	if listCalls == 0 {
		t.Fatal("list was not called")
	}

	fileOutput := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileOutput, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := pullDrops(cmd, server.URL, "session", []string{"one", "two"}, fileOutput, false); err == nil || !strings.Contains(err.Error(), "existing directory") {
		t.Fatalf("pullDrops(multi file output) error = %v", err)
	}

	stdout, _, err = executeRoot(t, "config", "path")
	if err != nil || strings.TrimSpace(stdout) == "" {
		t.Fatalf("config path stdout=%q err=%v", stdout, err)
	}
	for _, set := range [][]string{{"endpoint", server.URL}, {"default_ttl", "2h"}, {"clipboard", "false"}, {"machine_name", "desk"}} {
		if _, _, err := executeRoot(t, append([]string{"config", "set"}, set...)...); err != nil {
			t.Fatalf("config set %v error = %v", set, err)
		}
	}
	for _, set := range [][]string{{"default_ttl", "bad"}, {"clipboard", "maybe"}, {"unknown", "x"}} {
		if _, _, err := executeRoot(t, append([]string{"config", "set"}, set...)...); err == nil {
			t.Fatalf("config set %v error = nil, want error", set)
		}
	}

	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeRoot(t, "doctor")
	if err != nil || !strings.Contains(stdout, "ok") || !strings.Contains(stderr, "chain: missing") {
		t.Fatalf("doctor missing stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestWaitForNewImageDropErrorBranches(t *testing.T) {
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":"watch bad"}`)
	}))
	defer errorServer.Close()
	if _, err := waitForNewImageDrop(context.Background(), errorServer.URL, "session", time.Second); err == nil || !strings.Contains(err.Error(), "watch bad") {
		t.Fatalf("wait initial error = %v", err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := waitForNewImageDrop(ctx, errorServer.URL, "session", time.Second); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("wait initial deadline error = %v", err)
	}

	var calls int
	loopErrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = fmt.Fprint(w, `{"drops":[]}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":"loop bad"}`)
	}))
	defer loopErrorServer.Close()
	oldInterval := pullWatchPollInterval
	pullWatchPollInterval = time.Millisecond
	defer func() { pullWatchPollInterval = oldInterval }()
	if _, err := waitForNewImageDrop(context.Background(), loopErrorServer.URL, "session", time.Second); err == nil || !strings.Contains(err.Error(), "loop bad") {
		t.Fatalf("wait loop error = %v", err)
	}

	ready := make(chan struct{})
	var once sync.Once
	cancelCtx, cancelWait := context.WithCancel(context.Background())
	cancelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(ready) })
		_, _ = fmt.Fprint(w, `{"drops":[]}`)
	}))
	defer cancelServer.Close()
	done := make(chan error, 1)
	pullWatchPollInterval = time.Hour
	go func() {
		_, err := waitForNewImageDrop(cancelCtx, cancelServer.URL, "session", 0)
		done <- err
	}()
	<-ready
	cancelWait()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait canceled error = %v", err)
	}
}

func TestWatchForNewImageDrop(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = fmt.Fprint(w, `{"drops":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"drops":[{"id":"img","url":"http://example.test/d/img","filename":"img.png","content_type":"image/png","size":1,"created_at":"2026-05-23T12:00:00Z","expires_at":"2026-05-23T13:00:00Z"}]}`)
	}))
	defer server.Close()
	oldInterval := pullWatchPollInterval
	pullWatchPollInterval = time.Millisecond
	defer func() { pullWatchPollInterval = oldInterval }()
	id, err := waitForNewImageDrop(context.Background(), server.URL, "session", time.Second)
	if err != nil || id != "img" {
		t.Fatalf("waitForNewImageDrop() = %q, %v", id, err)
	}

	timeoutServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"drops":[]}`)
	}))
	defer timeoutServer.Close()
	if _, err := waitForNewImageDrop(context.Background(), timeoutServer.URL, "session", time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("waitForNewImageDrop(timeout) error = %v", err)
	}
}

func TestRedactAndExecute(t *testing.T) {
	useTempCLIConfig(t)
	oldArgs := os.Args
	os.Args = []string{"context-drop", "version"}
	defer func() { os.Args = oldArgs }()
	if err := Execute(BuildInfo{Version: "test"}); err != nil {
		t.Fatalf("Execute(version) error = %v", err)
	}
	if got := redact(""); got != "" {
		t.Fatalf("redact(empty) = %q", got)
	}
	if got := redact("short"); got != "<redacted>" {
		t.Fatalf("redact(short) = %q", got)
	}
	if got := redact("abcdefghijklmnop"); got != "abcdef...mnop" {
		t.Fatalf("redact(long) = %q", got)
	}
	var parsed linkOnlyResponse
	if err := json.Unmarshal([]byte(`{"url":"https://example.test"}`), &parsed); err != nil || parsed.URL == "" {
		t.Fatalf("linkOnlyResponse json = %+v, %v", parsed, err)
	}
}
