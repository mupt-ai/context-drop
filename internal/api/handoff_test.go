package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/handoff"
	"contextdrop.dev/context-drop/internal/pairing"
	"contextdrop.dev/context-drop/internal/storage"
)

func TestHandoffRoundTripIsRecipientScoped(t *testing.T) {
	t.Parallel()
	store := pairing.NewMemory()
	now := time.Now().UTC()
	source, sourceToken, _, err := store.CreateChain(t.Context(), "laptop", now)
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := store.CreateInvite(t.Context(), source, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	target, targetToken, _, err := store.ConsumeInvite(t.Context(), invite, "server", now)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{BaseURL: "http://example.test", Store: storage.NewLocal(t.TempDir()), PairingStore: store, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, MaxBytes: 1024}).Handler()

	create := map[string]any{"to": target.MachineID, "summary": "inspect the test failure", "requested_action": "review only", "ttl": "1h"}
	data, _ := json.Marshal(create)
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+sourceToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var made handoff.Handoff
	if err := json.NewDecoder(rec.Body).Decode(&made); err != nil {
		t.Fatal(err)
	}
	if made.ID == "" {
		t.Fatalf("decoded empty handoff from body %s", rec.Body.String())
	}
	if direct, err := store.GetHandoff(t.Context(), target, made.ID, time.Now().UTC()); err != nil || direct.ID != made.ID {
		t.Fatalf("direct handoff lookup = %+v, %v", direct, err)
	}

	for _, tc := range []struct {
		name, token string
		want        int
	}{{"source cannot read recipient inbox", sourceToken, http.StatusNotFound}, {"target can read", targetToken, http.StatusOK}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/handoffs/"+made.ID, nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}

	stateBody := bytes.NewBufferString(`{"state":"inspected"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/handoffs/"+made.ID+"/state", stateBody)
	req.Header.Set("Authorization", "Bearer "+targetToken)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", rec.Code, rec.Body.String())
	}
	var inspected handoff.Handoff
	_ = json.NewDecoder(rec.Body).Decode(&inspected)
	if inspected.RecipientState != handoff.StateInspected {
		t.Fatalf("state=%s", inspected.RecipientState)
	}
}

func TestHandoffRejectsCrossChainArtifact(t *testing.T) {
	t.Parallel()
	server, token := newTestServer(t, time.Hour)
	h := server.Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs", bytes.NewBufferString(`{"to":"missing","summary":"x","artifacts":[{"drop_id":"AAAAAAAAAAAAAAAA"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
