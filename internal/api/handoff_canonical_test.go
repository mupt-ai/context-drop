package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/drop"
	"contextdrop.dev/context-drop/internal/handoff"
	"contextdrop.dev/context-drop/internal/pairing"
	"contextdrop.dev/context-drop/internal/storage"
)

func TestHandoffCanonicalizesArtifactMetadataAndTTL(t *testing.T) {
	store := pairing.NewMemory()
	now := time.Now().UTC()
	source, token, _, err := store.CreateChain(t.Context(), "source", now)
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := store.CreateInvite(t.Context(), source, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	target, _, _, err := store.ConsumeInvite(t.Context(), invite, "target", now)
	if err != nil {
		t.Fatal(err)
	}
	files := storage.NewLocal(t.TempDir())
	meta := drop.Metadata{ID: "abcdefghijklmnop", ObjectKey: "x", Filename: "real.log", ContentType: "text/plain", Size: 3, SHA256: "abc", ChainID: source.ChainID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := files.Put(t.Context(), meta, bytes.NewBufferString("abc")); err != nil {
		t.Fatal(err)
	}
	h := NewServer(Options{BaseURL: "http://example.test", Store: files, PairingStore: store, DefaultTTL: 30 * time.Minute, MaxTTL: 24 * time.Hour, MaxBytes: 1024}).Handler()
	body := `{"to":"` + target.MachineID + `","summary":"x","ttl":"30m","artifacts":[{"drop_id":"abcdefghijklmnop","filename":"lie","size":99,"content_type":"x/fake","sha256":"lie"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/handoffs", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	var made handoff.Handoff
	if err := json.NewDecoder(w.Body).Decode(&made); err != nil {
		t.Fatal(err)
	}
	a := made.Artifacts[0]
	if a.Filename != meta.Filename || a.Size != meta.Size || a.ContentType != meta.ContentType || a.SHA256 != meta.SHA256 {
		t.Fatalf("artifact=%+v", a)
	}
	body = `{"to":"` + target.MachineID + `","summary":"x","ttl":"2h","artifacts":[{"drop_id":"abcdefghijklmnop"}]}`
	r = httptest.NewRequest(http.MethodPost, "/v1/handoffs", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected expiry rejection: %d %s", w.Code, w.Body.String())
	}
}
