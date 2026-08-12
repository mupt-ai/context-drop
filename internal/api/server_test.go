package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/drop"
	"contextdrop.dev/context-drop/internal/storage"
)

const testUploadToken = "test-upload-token"

type stubStore struct {
	putErr  error
	getErr  error
	blobErr error
	meta    drop.Metadata
	blob    string
}

func (s *stubStore) Put(_ context.Context, meta drop.Metadata, body io.Reader) error {
	if s.putErr != nil {
		return s.putErr
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.meta = meta
	s.blob = string(data)
	return nil
}

func (s *stubStore) GetMeta(_ context.Context, _ string) (drop.Metadata, error) {
	if s.getErr != nil {
		return drop.Metadata{}, s.getErr
	}
	return s.meta, nil
}

func (s *stubStore) GetBlob(_ context.Context, _ drop.Metadata) (io.ReadCloser, error) {
	if s.blobErr != nil {
		return nil, s.blobErr
	}
	return io.NopCloser(strings.NewReader(s.blob)), nil
}

func newTestServer(t *testing.T, ttl time.Duration) *Server {
	t.Helper()
	return NewServer(Options{
		BaseURL:     "http://example.test",
		Store:       storage.NewLocal(t.TempDir()),
		UploadToken: testUploadToken,
		DefaultTTL:  ttl,
		MaxTTL:      24 * time.Hour,
		MaxBytes:    1024,
	})
}

func uploadRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testUploadToken)
	return req
}

func TestServer_ExposesOnlyHealthUploadAndPublicDownload(t *testing.T) {
	t.Parallel()

	h := newTestServer(t, time.Hour).Handler()
	for _, path := range []string{"/health", "/healthz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
			t.Fatalf("GET %s = %d %q", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s missing nosniff", path)
		}
	}

	legacy := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodPost, "/v1/chains"},
		{http.MethodPost, "/v1/invites"},
		{http.MethodPost, "/v1/join"},
		{http.MethodGet, "/v1/machines"},
		{http.MethodGet, "/v1/messages"},
		{http.MethodPost, "/v1/handoffs"},
		{http.MethodGet, "/v1/drops"},
		{http.MethodDelete, "/v1/drops/ABCDEFGHIJKLMNOP"},
		{http.MethodGet, "/v1/drops/ABCDEFGHIJKLMNOP/blob"},
	}
	for _, tt := range legacy {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want 404 or 405", tt.method, tt.path, rec.Code)
		}
	}
}

func TestServer_UploadAndPublicDownloadEndToEnd(t *testing.T) {
	t.Parallel()

	h := newTestServer(t, time.Hour).Handler()
	body := []byte("hello from context-drop")
	req := httptest.NewRequest(http.MethodPost, "/v1/drops", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testUploadToken)
	req.Header.Set("X-Filename", "../hello.txt")
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created createDropResponse
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !drop.ValidID(created.ID) || created.URL != "http://example.test/d/"+created.ID || created.ContentType != "text/plain" || created.Size != int64(len(body)) {
		t.Fatalf("unexpected upload response: %+v", created)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+created.ID, nil))
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") || !strings.Contains(got, "hello.txt") {
		t.Fatalf("Content-Disposition = %q", got)
	}
}

func TestServer_UploadRequiresExactBearerToken(t *testing.T) {
	t.Parallel()

	h := newTestServer(t, time.Hour).Handler()
	for _, authorization := range []string{"", "Bearer wrong", "Basic " + testUploadToken, "bearer " + testUploadToken} {
		req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("hello"))
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization %q status = %d", authorization, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("missing WWW-Authenticate header")
		}
	}
}

func TestServer_MetadataHasNoChainOwnership(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	server := NewServer(Options{BaseURL: "http://example.test", Store: store, UploadToken: testUploadToken, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: 1024})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, uploadRequest("hello"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data, err := json.Marshal(store.meta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "chain") || store.meta.ObjectKey == "" || store.meta.SHA256 == "" {
		t.Fatalf("metadata = %s", data)
	}
}

func TestServer_UploadValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		ttl    string
		max    int64
		status int
	}{
		{name: "invalid ttl", body: "hello", ttl: "bad", max: 1024, status: http.StatusBadRequest},
		{name: "zero ttl", body: "hello", ttl: "0s", max: 1024, status: http.StatusBadRequest},
		{name: "too long ttl", body: "hello", ttl: "48h", max: 1024, status: http.StatusBadRequest},
		{name: "empty body", body: "", max: 1024, status: http.StatusBadRequest},
		{name: "too large", body: "hello", max: 3, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(Options{BaseURL: "http://example.test", Store: storage.NewLocal(t.TempDir()), UploadToken: testUploadToken, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: tt.max})
			req := uploadRequest(tt.body)
			if tt.ttl != "" {
				req.Header.Set("X-TTL", tt.ttl)
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tt.status)
			}
		})
	}
}

func TestServer_DownloadExpiryAndErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		store  *stubStore
		path   string
		status int
	}{
		{name: "invalid id", store: &stubStore{}, path: "/d/bad", status: http.StatusNotFound},
		{name: "missing", store: &stubStore{getErr: storage.ErrNotFound}, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusNotFound},
		{name: "metadata error", store: &stubStore{getErr: errors.New("boom")}, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusInternalServerError},
		{name: "expired", store: &stubStore{meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(-time.Hour)}}, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusGone},
		{name: "blob missing", store: &stubStore{meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(time.Hour)}, blobErr: storage.ErrNotFound}, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusNotFound},
		{name: "blob error", store: &stubStore{meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(time.Hour)}, blobErr: errors.New("boom")}, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(Options{BaseURL: "http://example.test", Store: tt.store, UploadToken: testUploadToken, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: 1024, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tt.status)
			}
		})
	}
}

func TestServer_StorePutError(t *testing.T) {
	t.Parallel()
	server := NewServer(Options{BaseURL: "http://example.test", Store: &stubStore{putErr: errors.New("boom")}, UploadToken: testUploadToken, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: 1024, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, uploadRequest("hello"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestContentDispositionUsesAttachmentForUnsafeTypes(t *testing.T) {
	t.Parallel()
	got := contentDisposition("application/octet-stream", "../../unsafe.bin")
	if !strings.HasPrefix(got, "attachment;") || !strings.Contains(got, "unsafe.bin") {
		t.Fatalf("contentDisposition() = %q", got)
	}
}

func TestParseTTLValidExplicitValue(t *testing.T) {
	t.Parallel()
	got, err := newTestServer(t, time.Hour).parseTTL("30m")
	if err != nil || got != 30*time.Minute {
		t.Fatalf("parseTTL() = %s, %v", got, err)
	}
}
