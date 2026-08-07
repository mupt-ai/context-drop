package api

import (
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

type stubStore struct {
	putErr    error
	getErr    error
	blobErr   error
	listErr   error
	deleteErr error
	meta      drop.Metadata
	metas     []drop.Metadata
	blob      string
	deletedID string
}

func (s *stubStore) Put(ctx context.Context, meta drop.Metadata, body io.Reader) error {
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

func (s *stubStore) GetMeta(ctx context.Context, id string) (drop.Metadata, error) {
	if s.getErr != nil {
		return drop.Metadata{}, s.getErr
	}
	return s.meta, nil
}

func (s *stubStore) GetBlob(ctx context.Context, meta drop.Metadata) (io.ReadCloser, error) {
	if s.blobErr != nil {
		return nil, s.blobErr
	}
	return io.NopCloser(strings.NewReader(s.blob)), nil
}

func (s *stubStore) List(ctx context.Context, chainID string) ([]drop.Metadata, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.metas, nil
}

func (s *stubStore) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedID = id
	return nil
}

func TestServer_HealthEndpointsSetSecureHeaders(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, time.Hour)
	for _, path := range []string{"/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if rec.Body.String() != "ok\n" {
				t.Fatalf("body = %q, want ok", rec.Body.String())
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
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
		{name: "too large read error", body: "hello", max: 3, status: http.StatusRequestEntityTooLarge},
		{name: "too large exact limit", body: "abcd", max: 3, status: http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pairingStore, token, _ := newTestPairingStore(t, "machine")
			server := NewServer(Options{
				BaseURL:      "http://example.test",
				Store:        storage.NewLocal(t.TempDir()),
				PairingStore: pairingStore,
				DefaultTTL:   time.Hour,
				MaxTTL:       24 * time.Hour,
				MaxBytes:     tt.max,
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
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

func TestServer_PublicDownloadAndDeleteDrop(t *testing.T) {
	t.Parallel()

	server, token := newTestServer(t, time.Hour)
	h := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Filename", "../hello.png")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created createDropResponse
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ContentType != "image/png" {
		t.Fatalf("ContentType = %q, want image/png", created.ContentType)
	}

	req = httptest.NewRequest(http.MethodGet, "/d/"+created.ID+"/hello.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q, want hello", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/drops/"+created.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/d/"+created.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_ListSkipsExpiredDrops(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	pairingStore, token, chainID := newTestPairingStore(t, "machine")
	server := NewServer(Options{
		BaseURL: "http://example.test",
		Store: &stubStore{metas: []drop.Metadata{
			{ID: "ABCDEFGHIJKLMNOP", Filename: "fresh.txt", ExpiresAt: now.Add(time.Hour), CreatedAt: now, ChainID: chainID},
			{ID: "QRSTUVWXYZabcdef", Filename: "expired.txt", ExpiresAt: now.Add(-time.Hour), CreatedAt: now, ChainID: chainID},
		}},
		PairingStore: pairingStore,
		DefaultTTL:   time.Hour,
		MaxTTL:       24 * time.Hour,
		MaxBytes:     1024,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/drops", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "expired.txt") || !strings.Contains(rec.Body.String(), "fresh.txt") {
		t.Fatalf("list body = %s, want only fresh drop", rec.Body.String())
	}
}

func TestServer_StoreErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		store  *stubStore
		method string
		path   string
		body   string
		status int
	}{
		{name: "put error", store: &stubStore{putErr: errors.New("boom")}, method: http.MethodPost, path: "/v1/drops", body: "hello", status: http.StatusInternalServerError},
		{name: "list error", store: &stubStore{listErr: errors.New("boom")}, method: http.MethodGet, path: "/v1/drops", status: http.StatusInternalServerError},
		{name: "get metadata not found", store: &stubStore{getErr: storage.ErrNotFound}, method: http.MethodGet, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusNotFound},
		{name: "get metadata error", store: &stubStore{getErr: errors.New("boom")}, method: http.MethodGet, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusInternalServerError},
		{name: "blob not found", store: &stubStore{meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(time.Hour)}, blobErr: storage.ErrNotFound}, method: http.MethodGet, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusNotFound},
		{name: "blob error", store: &stubStore{meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(time.Hour)}, blobErr: errors.New("boom")}, method: http.MethodGet, path: "/d/ABCDEFGHIJKLMNOP", status: http.StatusInternalServerError},
		{name: "delete error", store: &stubStore{meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(time.Hour)}, deleteErr: errors.New("boom")}, method: http.MethodDelete, path: "/v1/drops/ABCDEFGHIJKLMNOP", status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pairingStore, token, chainID := newTestPairingStore(t, "machine")
			if tt.store.meta.ID != "" && tt.store.meta.ChainID == "" {
				tt.store.meta.ChainID = chainID
			}
			server := NewServer(Options{
				BaseURL:      "http://example.test",
				Store:        tt.store,
				PairingStore: pairingStore,
				DefaultTTL:   time.Hour,
				MaxTTL:       24 * time.Hour,
				MaxBytes:     1024,
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tt.status)
			}
		})
	}
}

func TestServer_AuthenticatedDropPathErrors(t *testing.T) {
	t.Parallel()

	server, token := newTestServer(t, time.Hour)

	tests := []struct {
		name   string
		path   string
		token  string
		status int
	}{
		{name: "missing blob suffix", path: "/v1/drops/ABCDEFGHIJKLMNOP", token: token, status: http.StatusNotFound},
		{name: "unauthorized", path: "/v1/drops/ABCDEFGHIJKLMNOP/blob", status: http.StatusUnauthorized},
		{name: "invalid id", path: "/v1/drops/bad/blob", token: token, status: http.StatusNotFound},
		{name: "public invalid id", path: "/d/bad", status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tt.status)
			}
		})
	}
}

func TestServer_ListAndDeleteRequireAuthentication(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, time.Hour)
	for _, tt := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v1/drops"},
		{method: http.MethodDelete, path: "/v1/drops/ABCDEFGHIJKLMNOP"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusUnauthorized)
			}
		})
	}
}

func TestServer_AuthenticatedDropMetadataErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		store  *stubStore
		method string
		path   string
		status int
	}{
		{name: "pull not found", store: &stubStore{getErr: storage.ErrNotFound}, method: http.MethodGet, path: "/v1/drops/ABCDEFGHIJKLMNOP/blob", status: http.StatusNotFound},
		{name: "pull store error", store: &stubStore{getErr: errors.New("boom")}, method: http.MethodGet, path: "/v1/drops/ABCDEFGHIJKLMNOP/blob", status: http.StatusInternalServerError},
		{name: "delete load failed", store: &stubStore{getErr: storage.ErrNotFound}, method: http.MethodDelete, path: "/v1/drops/ABCDEFGHIJKLMNOP", status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pairingStore, token, _ := newTestPairingStore(t, "machine")
			server := NewServer(Options{
				BaseURL:      "http://example.test",
				Store:        tt.store,
				PairingStore: pairingStore,
				DefaultTTL:   time.Hour,
				MaxTTL:       24 * time.Hour,
				MaxBytes:     1024,
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tt.status)
			}
		})
	}
}

func TestServer_LoadOwnedDropRejectsWrongOwnerAndExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta drop.Metadata
		want int
	}{
		{name: "wrong owner", meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ChainID: "other", ExpiresAt: time.Now().Add(time.Hour)}, want: http.StatusNotFound},
		{name: "expired", meta: drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(-time.Hour)}, want: http.StatusGone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pairingStore, token, chainID := newTestPairingStore(t, "machine")
			meta := tt.meta
			if meta.ChainID == "" {
				meta.ChainID = chainID
			}
			server := NewServer(Options{
				BaseURL:      "http://example.test",
				Store:        &stubStore{meta: meta},
				PairingStore: pairingStore,
				DefaultTTL:   time.Hour,
				MaxTTL:       24 * time.Hour,
				MaxBytes:     1024,
			})
			req := httptest.NewRequest(http.MethodGet, "/v1/drops/ABCDEFGHIJKLMNOP/blob", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestContentDispositionUsesAttachmentForUnsafeTypes(t *testing.T) {
	t.Parallel()

	got := contentDisposition("application/octet-stream", "../../unsafe.bin")
	if !strings.HasPrefix(got, "attachment;") || !strings.Contains(got, "unsafe.bin") {
		t.Fatalf("contentDisposition() = %q, want attachment with safe filename", got)
	}
	if isSafeInline("application/octet-stream") {
		t.Fatal("isSafeInline(octet-stream) = true, want false")
	}
}

func TestParseTTLValidExplicitValue(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, time.Hour)
	got, err := server.parseTTL("30m")
	if err != nil {
		t.Fatal(err)
	}
	if got != 30*time.Minute {
		t.Fatalf("parseTTL() = %s, want 30m", got)
	}
}
