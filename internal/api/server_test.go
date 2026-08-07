package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/pairing"
	"contextdrop.dev/context-drop/internal/storage"
)

func newTestPairingStore(t *testing.T, machineName string) (pairing.Store, string, string) {
	t.Helper()
	pairingStore := pairing.NewMemory()
	principal, token, _, err := pairingStore.CreateChain(t.Context(), machineName, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return pairingStore, token, principal.ChainID
}

func newTestServer(t *testing.T, ttl time.Duration) (*Server, string) {
	t.Helper()
	pairingStore, token, _ := newTestPairingStore(t, "machine")
	server := NewServer(Options{
		BaseURL:      "http://example.test",
		Store:        storage.NewLocal(t.TempDir()),
		PairingStore: pairingStore,
		DefaultTTL:   ttl,
		MaxTTL:       24 * time.Hour,
		MaxBytes:     1024,
	})
	return server, token
}

func TestServer_RootRedirectsToRepository(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer(t, time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "https://github.com/mupt-ai/context-drop" {
		t.Fatalf("Location = %q", got)
	}
}

func TestServer_LocalStorageEndToEnd(t *testing.T) {
	t.Parallel()

	server, token := newTestServer(t, time.Hour)
	h := server.Handler()

	body := []byte("hello from context-drop")
	req := httptest.NewRequest(http.MethodPost, "/v1/drops", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Filename", "hello.txt")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/drops status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var upload struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	if upload.ID == "" || !strings.HasPrefix(upload.URL, "http://example.test/d/") {
		t.Fatalf("unexpected upload response: %+v", upload)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/drops", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/drops status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Drops []struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
		} `json:"drops"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Drops) != 1 || list.Drops[0].ID != upload.ID || list.Drops[0].Filename != "hello.txt" {
		t.Fatalf("unexpected list response: %+v", list)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/drops/"+upload.ID+"/blob", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/drops/{id}/blob status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("download body = %q, want %q", got, body)
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Fatalf("missing content disposition")
	}
}

func TestServer_DropsAreScopedToChain(t *testing.T) {
	t.Parallel()

	pairingStore := pairing.NewMemory()
	_, token1, _, err := pairingStore.CreateChain(t.Context(), "one", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, token2, _, err := pairingStore.CreateChain(t.Context(), "two", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{
		BaseURL:      "http://example.test",
		Store:        storage.NewLocal(t.TempDir()),
		PairingStore: pairingStore,
		DefaultTTL:   time.Hour,
		MaxTTL:       24 * time.Hour,
		MaxBytes:     1024,
	})
	h := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("secret"))
	req.Header.Set("Authorization", "Bearer "+token1)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var upload struct{ ID string }
	if err := json.NewDecoder(rec.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/drops", nil)
	req.Header.Set("Authorization", "Bearer "+token2)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Drops []struct{} `json:"drops"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Drops) != 0 {
		t.Fatalf("chain 2 saw chain 1 drops: %+v", list)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/drops/"+upload.ID+"/blob", nil)
	req.Header.Set("Authorization", "Bearer "+token2)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pull status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_CreateChainAndRejectsUnauthorizedUpload(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		BaseURL:    "http://example.test",
		Store:      storage.NewLocal(t.TempDir()),
		DefaultTTL: time.Hour,
		MaxTTL:     24 * time.Hour,
		MaxBytes:   1024,
	})
	h := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/chains", strings.NewReader(`{"machine_name":"laptop"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("init status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var init struct {
		ChainID      string `json:"chain_id"`
		MachineName  string `json:"machine_name"`
		SessionToken string `json:"session_token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&init); err != nil {
		t.Fatal(err)
	}
	if init.ChainID == "" || init.MachineName != "laptop" || init.SessionToken == "" {
		t.Fatalf("unexpected init response: %+v", init)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("hello"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServer_ExpiredDropReturnsGone(t *testing.T) {
	t.Parallel()

	server, token := newTestServer(t, time.Nanosecond)
	h := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/drops", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var upload struct{ ID string }
	if err := json.NewDecoder(rec.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)

	req = httptest.NewRequest(http.MethodGet, "/d/"+upload.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusGone)
	}
}
