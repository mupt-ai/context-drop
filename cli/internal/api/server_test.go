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

func TestPublicServerUploadAndDownload(t *testing.T) {
	s := NewServer(Options{
		Store:      storage.NewLocal(t.TempDir()),
		DefaultTTL: time.Hour, MaxTTL: time.Hour, MaxBytes: 1024,
	})
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("public upload")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}
	var created createDropResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, created.URL, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "public upload" {
		t.Fatalf("download = %d: %s", rec.Code, rec.Body.String())
	}
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

func TestPublicUploadsAreLimitedPerClient(t *testing.T) {
	s := NewServer(Options{
		Store:      storage.NewLocal(t.TempDir()),
		DefaultTTL: time.Hour, MaxTTL: time.Hour, MaxBytes: 1024,
		UploadsPerHour: 2, UploadBytesPerHour: 10, ProxyHops: 1,
	})
	h := s.Handler()
	upload := func(ip, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader(body))
		// The first entry is client-supplied and must not pick the bucket.
		req.Header.Set("X-Forwarded-For", "203.0.113.9, "+ip)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i, want := range []int{http.StatusCreated, http.StatusCreated, http.StatusTooManyRequests} {
		if rec := upload("198.51.100.1", "abc"); rec.Code != want {
			t.Fatalf("upload %d = %d, want %d: %s", i, rec.Code, want, rec.Body.String())
		}
	}
	rec := upload("198.51.100.1", "abc")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Another client has its own budget, and the byte cap applies too. A
	// rejected body still counts, so retrying it does not reset anything.
	if rec := upload("198.51.100.2", "0123456789"); rec.Code != http.StatusCreated {
		t.Fatalf("second client = %d, want 201", rec.Code)
	}
	if rec := upload("198.51.100.3", "0123456789x"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget bytes = %d, want 429", rec.Code)
	}
	if rec := upload("198.51.100.3", "x"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after a rejected body = %d, want 429", rec.Code)
	}
}

func TestTokenUploadsAreNotLimited(t *testing.T) {
	s := NewServer(Options{
		Store: storage.NewLocal(t.TempDir()), UploadToken: testUploadToken,
		DefaultTTL: time.Hour, MaxTTL: time.Hour, MaxBytes: 1024, UploadsPerHour: 1,
	})
	h := s.Handler()
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, uploadRequest("x"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %d = %d", i, rec.Code)
		}
	}
}

func TestUploadCountIsReservedBeforeTheBody(t *testing.T) {
	l := newUploadLimiter(2, 0, time.Hour)
	// Three uploads start before any finishes: the third must be refused.
	for i, want := range []bool{true, true, false} {
		if ok, _ := l.allow("c"); ok != want {
			t.Fatalf("allow %d = %v, want %v", i, ok, want)
		}
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/drops", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		hops int
		want string
	}{
		{"direct", req("192.0.2.7:5000", "203.0.113.1"), 0, "192.0.2.7"},
		{"one proxy", req("10.0.0.1:5000", "6.6.6.6, 203.0.113.1"), 1, "203.0.113.1"},
		{"load balancer", req("10.0.0.1:5000", "6.6.6.6, 203.0.113.1, 35.1.2.3"), 2, "203.0.113.1"},
		{"too few entries", req("10.0.0.1:5000", "203.0.113.1"), 2, "10.0.0.1"},
		{"ipv6 /64", req("[2001:db8:1:2:aaaa::1]:5000", ""), 0, "2001:db8:1:2::/64"},
	}
	for _, c := range cases {
		if got := clientIP(c.r, c.hops); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOversizedUploadsCountAgainstTheByteBudget(t *testing.T) {
	s := NewServer(Options{
		Store:      storage.NewLocal(t.TempDir()),
		DefaultTTL: time.Hour, MaxTTL: time.Hour, MaxBytes: 4,
		UploadsPerHour: 100, UploadBytesPerHour: 8,
	})
	h := s.Handler()
	codes := []int{}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("0123456789")))
		codes = append(codes, rec.Code)
	}
	// The first oversized body is read up to the cap and charged; once the
	// budget is spent, further attempts are refused before their body is read.
	if codes[0] != http.StatusRequestEntityTooLarge || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
}
