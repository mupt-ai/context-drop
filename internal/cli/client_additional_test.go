package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUploadValidationAndRequest(t *testing.T) {
	if _, err := Upload(context.Background(), UploadRequest{UploadToken: "token", Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("endpoint error = %v", err)
	}
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: "https://example.test", Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "upload token") {
		t.Fatalf("token error = %v", err)
	}
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: "https://example.test", UploadToken: "token"}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/drops" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Filename"); got != "hello.txt" {
			t.Fatalf("filename = %q", got)
		}
		if got := r.Header.Get("X-TTL"); got != "30m0s" {
			t.Fatalf("ttl = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop-1","url":"http://example.test/d/drop-1","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
	}))
	defer server.Close()
	resp, err := Upload(context.Background(), UploadRequest{Endpoint: server.URL, UploadToken: "token", Filename: "hello.txt", ContentType: "text/plain", TTL: 30 * time.Minute, Data: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "drop-1" || resp.Size != 5 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestUploadErrors(t *testing.T) {
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: "http://[::1", UploadToken: "token", Data: []byte("x")}); err == nil {
		t.Fatal("expected bad URL error")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = fmt.Fprint(w, `{"msg":"bad drops"}`)
	}))
	defer server.Close()
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: server.URL, UploadToken: "token", Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "bad drops") {
		t.Fatalf("server error = %v", err)
	}
}
