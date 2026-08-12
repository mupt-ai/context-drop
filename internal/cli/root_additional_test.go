package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/config"
	"github.com/spf13/cobra"
)

func TestUploadInputHelpers(t *testing.T) {
	if _, _, _, err := inputData(nil, false); err == nil || !strings.Contains(err.Error(), "no path") {
		t.Fatalf("no path error = %v", err)
	}
	if _, _, _, err := inputData([]string{filepath.Join(t.TempDir(), "missing")}, false); err == nil {
		t.Fatal("expected missing file error")
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
		t.Fatalf("input = %q %q %q", data, name, contentType)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestUploadCommandJSONAndFlags(t *testing.T) {
	useTempCLIConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Filename"); got != "custom.log" {
			t.Fatalf("filename = %q", got)
		}
		if got := r.Header.Get("X-TTL"); got != "30m0s" {
			t.Fatalf("ttl = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "text/custom" {
			t.Fatalf("content type = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop-json","url":"http://example.test/d/drop-json","expires_at":"2026-05-23T12:00:00Z","content_type":"text/custom","size":5}`)
	}))
	defer server.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: server.URL, UploadToken: "secret", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeRoot(t, "upload", "--ttl", "30m", "--filename", "custom.log", "--content-type", "text/custom", "--json", file)
	if err != nil || !strings.Contains(stdout, "drop-json") {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
}

func TestRunUploadErrorAndClipboardBranches(t *testing.T) {
	useTempCLIConfig(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := runUpload(context.Background(), cmd, &options{clipboard: true, noClipboard: true}, nil); err == nil || !strings.Contains(err.Error(), "--clipboard") {
		t.Fatalf("conflict error = %v", err)
	}
	if got, err := effectiveClipboard(nil, config.CLIConfig{Clipboard: true}); err != nil || !got {
		t.Fatalf("effective clipboard = %v %v", got, err)
	}
	if got, err := effectiveClipboard(&options{noCopy: true}, config.CLIConfig{Clipboard: true}); err != nil || got {
		t.Fatalf("no copy = %v %v", got, err)
	}
	copyURLToClipboardIfRequested(cmd, false, "https://example.test")
}

func TestRunUploadServerAndWriterErrors(t *testing.T) {
	useTempCLIConfig(t)
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":"upload bad"}`)
	}))
	defer errorServer.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: errorServer.URL, UploadToken: "secret", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRoot(t, "upload", file); err == nil || !strings.Contains(err.Error(), "upload bad") {
		t.Fatalf("server error = %v", err)
	}

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop","url":"http://example.test/d/drop","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
	}))
	defer okServer.Close()
	if err := config.SaveCLIConfig(config.CLIConfig{Endpoint: okServer.URL, UploadToken: "secret", DefaultTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(errorWriter{})
	cmd.SetErr(&bytes.Buffer{})
	if err := runUpload(context.Background(), cmd, &options{json: true}, []string{file}); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("writer error = %v", err)
	}
}

func TestMimeTypeByExt(t *testing.T) {
	cases := map[string]string{"a.jpg": "image/jpeg", "a.gif": "image/gif", "a.webp": "image/webp", "a.pdf": "application/pdf", "a.md": "text/plain", "a.bin": ""}
	for name, want := range cases {
		if got := mimeTypeByExt(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}
