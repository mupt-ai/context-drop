package pairing

import (
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

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func TestMemoryStoreWorkflowAndErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemory()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	principal, sessionToken, owner, err := store.CreateChain(ctx, " Owner\nMachine ", now)
	if err != nil {
		t.Fatal(err)
	}
	if principal.ChainID == "" || principal.MachineID != owner.ID || sessionToken == "" {
		t.Fatalf("owner principal/session/machine = %+v %q %+v", principal, sessionToken, owner)
	}
	if owner.Name != "Owner_Machine" {
		t.Fatalf("owner name = %q, want cleaned name", owner.Name)
	}

	if _, ok, err := store.AuthenticateSession(ctx, " ", now); err != nil || ok {
		t.Fatalf("AuthenticateSession(empty) = ok %v err %v, want not ok", ok, err)
	}
	if _, ok, err := store.AuthenticateSession(ctx, "not-a-session", now); err != nil || ok {
		t.Fatalf("AuthenticateSession(invalid) = ok %v err %v, want not ok", ok, err)
	}
	authed, ok, err := store.AuthenticateSession(ctx, sessionToken, now.Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("AuthenticateSession(valid) = %+v ok %v err %v", authed, ok, err)
	}
	if authed.ChainID != principal.ChainID || authed.MachineID != owner.ID {
		t.Fatalf("authenticated principal = %+v, want chain %q machine %q", authed, principal.ChainID, owner.ID)
	}

	if _, _, err := store.CreateInvite(ctx, principal, -time.Second, now); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("CreateInvite(negative ttl) error = %v, want positive ttl error", err)
	}
	if _, _, err := store.CreateInvite(ctx, principal, MaxInviteTTL+time.Second, now); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("CreateInvite(too long ttl) error = %v, want exceeds error", err)
	}
	if _, _, err := store.CreateInvite(ctx, Principal{}, time.Minute, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CreateInvite(unauthorized) error = %v, want unauthorized", err)
	}

	defaultToken, defaultInvite, err := store.CreateInvite(ctx, principal, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if defaultToken == "" || !defaultInvite.ExpiresAt.Equal(now.Add(DefaultInviteTTL)) {
		t.Fatalf("default invite = token %q invite %+v", defaultToken, defaultInvite)
	}
	_, _, _, err = store.ConsumeInvite(ctx, " ", "empty", now)
	if !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("ConsumeInvite(empty token) error = %v, want invalid invite", err)
	}

	expiringToken, _, err := store.CreateInvite(ctx, principal, time.Nanosecond, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ConsumeInvite(ctx, expiringToken, "late", now.Add(time.Second))
	if !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("ConsumeInvite(expired) error = %v, want invalid invite", err)
	}

	joinToken, _, err := store.CreateInvite(ctx, principal, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	joined, joinedSession, laptop, err := store.ConsumeInvite(ctx, joinToken, "laptop", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if joined.ChainID != principal.ChainID || joined.MachineID != laptop.ID || joinedSession == "" || laptop.Name != "laptop" {
		t.Fatalf("joined = principal %+v token %q machine %+v", joined, joinedSession, laptop)
	}
	_, _, _, err = store.ConsumeInvite(ctx, joinToken, "reuse", now.Add(4*time.Second))
	if !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("ConsumeInvite(reuse) error = %v, want invalid invite", err)
	}

	machines, err := store.ListMachines(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 2 || machines[0].ID != owner.ID || machines[1].ID != laptop.ID {
		t.Fatalf("machines = %+v, want owner then laptop", machines)
	}
	if _, err := store.ListMachines(ctx, Principal{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ListMachines(unauthorized) error = %v, want unauthorized", err)
	}

	if _, err := store.SendMessage(ctx, principal, laptop.ID, " ", now); !errors.Is(err, ErrMessageEmpty) {
		t.Fatalf("SendMessage(empty) error = %v, want empty", err)
	}
	tooLong := strings.Repeat("x", maxMessageBody+1)
	if _, err := store.SendMessage(ctx, principal, laptop.ID, tooLong, now); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("SendMessage(too long) error = %v, want exceeds", err)
	}
	if _, err := store.SendMessage(ctx, Principal{}, laptop.ID, "hello", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("SendMessage(unauthorized) error = %v, want unauthorized", err)
	}
	if _, err := store.SendMessage(ctx, principal, "missing", "hello", now); !errors.Is(err, ErrMachineNotFound) {
		t.Fatalf("SendMessage(missing) error = %v, want machine not found", err)
	}
	if _, err := store.SendMessage(ctx, principal, " ", "hello", now); !errors.Is(err, ErrMachineNotFound) {
		t.Fatalf("SendMessage(empty recipient) error = %v, want machine not found", err)
	}

	secondToken, _, err := store.CreateInvite(ctx, principal, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ConsumeInvite(ctx, secondToken, "laptop", now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage(ctx, principal, "laptop", "ambiguous", now); !errors.Is(err, ErrAmbiguousMachine) {
		t.Fatalf("SendMessage(ambiguous) error = %v, want ambiguous", err)
	}

	msg, err := store.SendMessage(ctx, principal, laptop.ID, " hello ", now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Body != "hello" || msg.FromMachineID != owner.ID || msg.ToMachineID != laptop.ID {
		t.Fatalf("message = %+v", msg)
	}
	secondMsg, err := store.SendMessage(ctx, principal, laptop.ID, "same time", now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if secondMsg.CreatedAt != msg.CreatedAt {
		t.Fatalf("second message time = %s, want %s", secondMsg.CreatedAt, msg.CreatedAt)
	}
	messages, err := store.ListMessages(ctx, joined)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID > messages[1].ID {
		t.Fatalf("messages = %+v, want two messages sorted by id", messages)
	}
	if _, err := store.ListMessages(ctx, Principal{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ListMessages(unauthorized) error = %v, want unauthorized", err)
	}
}

type staticBackend struct {
	st           state
	loadErr      error
	saveErr      error
	saveAttempts int
}

func (b *staticBackend) Load(ctx context.Context) (state, int64, bool, error) {
	if b.loadErr != nil {
		return state{}, 0, false, b.loadErr
	}
	return cloneState(b.st), 0, true, nil
}

func (b *staticBackend) Save(ctx context.Context, st state, generation int64, exists bool) error {
	b.saveAttempts++
	if b.saveErr != nil {
		return b.saveErr
	}
	b.st = cloneState(st)
	return nil
}

func TestJSONStoreCorruptStateAndBackendErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	var empty state
	initState(&empty)
	if empty.Version != 1 || empty.Chains == nil || empty.Machines == nil || empty.Sessions == nil || empty.Invites == nil || empty.Messages == nil {
		t.Fatalf("initState(empty) = %+v", empty)
	}

	missingChainStore := &JSONStore{backend: &staticBackend{st: state{
		Version:  1,
		Invites:  map[string]Invite{tokenHash("join-token"): {TokenHash: tokenHash("join-token"), ChainID: "missing", ExpiresAt: now.Add(time.Minute)}},
		Chains:   map[string]Chain{},
		Machines: map[string]Machine{},
		Sessions: map[string]Session{},
		Messages: map[string]Message{},
	}}}
	if _, _, _, err := missingChainStore.ConsumeInvite(ctx, "join-token", "machine", now); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("ConsumeInvite(missing chain) error = %v, want invalid invite", err)
	}

	missingMachineStore := &JSONStore{backend: &staticBackend{st: state{
		Version:  1,
		Chains:   map[string]Chain{"chain-1": {ID: "chain-1"}},
		Machines: map[string]Machine{},
		Sessions: map[string]Session{tokenHash("session-token"): {TokenHash: tokenHash("session-token"), ChainID: "chain-1", MachineID: "missing"}},
		Invites:  map[string]Invite{},
		Messages: map[string]Message{},
	}}}
	if _, ok, err := missingMachineStore.AuthenticateSession(ctx, "session-token", now); err != nil || ok {
		t.Fatalf("AuthenticateSession(missing machine) = ok %v err %v, want not ok", ok, err)
	}

	missingAuthChainStore := &JSONStore{backend: &staticBackend{st: state{
		Version:  1,
		Chains:   map[string]Chain{},
		Machines: map[string]Machine{"machine-1": {ID: "machine-1", ChainID: "chain-1"}},
		Sessions: map[string]Session{tokenHash("session-token"): {TokenHash: tokenHash("session-token"), ChainID: "chain-1", MachineID: "machine-1"}},
		Invites:  map[string]Invite{},
		Messages: map[string]Message{},
	}}}
	if _, ok, err := missingAuthChainStore.AuthenticateSession(ctx, "session-token", now); err != nil || ok {
		t.Fatalf("AuthenticateSession(missing chain) = ok %v err %v, want not ok", ok, err)
	}

	loadErr := errors.New("load failed")
	loadErrStore := &JSONStore{backend: &staticBackend{loadErr: loadErr}}
	if _, err := loadErrStore.ListMachines(ctx, Principal{ChainID: "chain-1", MachineID: "machine-1"}); !errors.Is(err, loadErr) {
		t.Fatalf("ListMachines(load error) = %v, want load error", err)
	}

	conflictBackend := &staticBackend{st: newState(), saveErr: ErrConflict}
	conflictStore := &JSONStore{backend: conflictBackend}
	_, _, _, err := conflictStore.CreateChain(ctx, "machine", now)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateChain(conflict) error = %v, want conflict", err)
	}
	if conflictBackend.saveAttempts != 5 {
		t.Fatalf("save attempts = %d, want 5", conflictBackend.saveAttempts)
	}

	if _, err := newID("test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newSession("chain-1", "machine-1", now); err != nil {
		t.Fatal(err)
	}
	if got := CleanMachineName("\x00\x7f"); got != "__" {
		t.Fatalf("CleanMachineName(control chars) = %q, want __", got)
	}
	if got := CleanMachineName(strings.Repeat("x", maxMachineName+10)); len(got) != maxMachineName {
		t.Fatalf("CleanMachineName(long) length = %d, want %d", len(got), maxMachineName)
	}
}

func TestMemoryBackendCloneAndConflicts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backend := newMemoryBackend()
	st, generation, exists, err := backend.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if exists || generation != 0 {
		t.Fatalf("initial Load() generation/exist = %d/%v, want 0/false", generation, exists)
	}
	st.Chains["chain-1"] = Chain{ID: "chain-1", CreatedAt: time.Now().UTC()}
	if err := backend.Save(ctx, st, generation, exists); err != nil {
		t.Fatal(err)
	}

	loaded, nextGeneration, nextExists, err := backend.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !nextExists || nextGeneration != 1 || loaded.Chains["chain-1"].ID != "chain-1" {
		t.Fatalf("loaded state = gen %d exists %v state %+v", nextGeneration, nextExists, loaded)
	}
	loaded.Chains["chain-1"] = Chain{ID: "mutated"}
	again, _, _, err := backend.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Chains["chain-1"].ID != "chain-1" {
		t.Fatalf("Load did not clone state: %+v", again.Chains["chain-1"])
	}
	if err := backend.Save(ctx, loaded, generation, exists); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save(stale generation) error = %v, want conflict", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := backend.Load(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) error = %v, want canceled", err)
	}
	if err := backend.Save(canceled, newState(), 0, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(canceled) error = %v, want canceled", err)
	}
}

func TestLocalBackendAdditionalErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, _, _, err := (&localBackend{path: dir}).Load(ctx); err == nil {
		t.Fatal("Load(directory) error = nil, want error")
	}

	badJSON := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(badJSON, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := (&localBackend{path: badJSON}).Load(ctx); err == nil {
		t.Fatal("Load(bad json) error = nil, want error")
	}

	path := filepath.Join(t.TempDir(), "pairing.json")
	backend := &localBackend{path: path}
	if err := backend.Save(ctx, newState(), 1, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save(existing missing) error = %v, want conflict", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backend.Save(ctx, newState(), 0, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save(new existing) error = %v, want conflict", err)
	}

	parentFile := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&localBackend{path: filepath.Join(parentFile, "pairing.json")}).Save(ctx, newState(), 0, false); err == nil {
		t.Fatal("Save(parent file) error = nil, want error")
	}
}

func TestLocalBackendErrorsAndConflicts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "pairing.json")
	backend := &localBackend{path: path}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := backend.Load(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) error = %v, want canceled", err)
	}
	if err := backend.Save(canceled, newState(), 0, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(canceled) error = %v, want canceled", err)
	}

	st, generation, exists, err := backend.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if exists || generation != 0 || st.Version != 1 {
		t.Fatalf("Load(missing) = gen %d exists %v state %+v", generation, exists, st)
	}
	st.Chains["chain-1"] = Chain{ID: "chain-1"}
	if err := backend.Save(ctx, st, generation, exists); err != nil {
		t.Fatal(err)
	}
	if err := backend.Save(ctx, st, generation, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save(create existing) error = %v, want conflict", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := backend.Save(ctx, st, generation, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save(update missing) error = %v, want conflict", err)
	}

	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badBackend := &localBackend{path: badPath}
	if _, _, _, err := badBackend.Load(ctx); err == nil {
		t.Fatal("Load(invalid json) error = nil, want error")
	}
}

func TestNewGCSUsesEmulator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)

	store, err := NewGCS(context.Background(), "bucket", "/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	backend, ok := store.backend.(*gcsBackend)
	if !ok {
		t.Fatalf("backend = %T, want *gcsBackend", store.backend)
	}
	defer backend.client.Close()
	if backend.bucket != "bucket" || backend.object != "prefix/pairing/state.json" {
		t.Fatalf("backend = %+v, want bucket and prefixed object", backend)
	}
}

func TestGCSBackendLoadSaveWithJSONAPI(t *testing.T) {
	t.Parallel()

	var sawSave bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/b/bucket/o/prefix%2Fpairing%2Fstate.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"bucket":"bucket","name":"prefix/pairing/state.json","generation":"7"}`)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/bucket/prefix%2Fpairing%2Fstate.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"version":1,"chains":{"chain-1":{"id":"chain-1","created_at":"2026-05-23T12:00:00Z"}}}`)
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/upload/storage/v1/b/bucket/o":
			sawSave = true
			if got := r.URL.Query().Get("ifGenerationMatch"); got != "7" {
				http.Error(w, "wrong generation", http.StatusBadRequest)
				return
			}
			if got := r.URL.Query().Get("name"); got != "prefix/pairing/state.json" {
				http.Error(w, "wrong object name", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"bucket":"bucket","name":"prefix/pairing/state.json","generation":"8"}`)
		default:
			http.Error(w, "unexpected GCS request: "+r.Method+" "+r.URL.String(), http.StatusTeapot)
		}
	}))
	defer server.Close()

	client, err := gcs.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	backend := &gcsBackend{client: client, bucket: "bucket", object: "prefix/pairing/state.json"}
	st, generation, exists, err := backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists || generation != 7 || st.Chains["chain-1"].ID != "chain-1" {
		t.Fatalf("Load() = gen %d exists %v state %+v", generation, exists, st)
	}
	st.Chains["chain-2"] = Chain{ID: "chain-2"}
	if err := backend.Save(context.Background(), st, generation, exists); err != nil {
		t.Fatal(err)
	}
	if !sawSave {
		t.Fatal("Save did not call upload endpoint")
	}
}

func TestGCSBackendAdditionalErrorPaths(t *testing.T) {
	t.Parallel()

	attrsErrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":400,"message":"boom"}}`, http.StatusBadRequest)
	}))
	defer attrsErrorServer.Close()
	attrsClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(attrsErrorServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	attrsBackend := &gcsBackend{client: attrsClient, bucket: "bucket", object: "pairing/state.json"}
	if _, _, _, err := attrsBackend.Load(context.Background()); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("Load(attrs error) = %v, want non-conflict error", err)
	}

	readerConflictServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), "/b/bucket/o/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"bucket":"bucket","name":"pairing/state.json","generation":"7"}`)
			return
		}
		http.Error(w, `{"error":{"code":412,"message":"precondition failed"}}`, http.StatusPreconditionFailed)
	}))
	defer readerConflictServer.Close()
	readerClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(readerConflictServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	readerBackend := &gcsBackend{client: readerClient, bucket: "bucket", object: "pairing/state.json"}
	if _, _, _, err := readerBackend.Load(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("Load(reader precondition) = %v, want conflict", err)
	}

	invalidJSONServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), "/b/bucket/o/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"bucket":"bucket","name":"pairing/state.json","generation":"7"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{`)
	}))
	defer invalidJSONServer.Close()
	invalidClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(invalidJSONServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	invalidBackend := &gcsBackend{client: invalidClient, bucket: "bucket", object: "pairing/state.json"}
	if _, _, _, err := invalidBackend.Load(context.Background()); err == nil {
		t.Fatal("Load(invalid JSON) error = nil, want error")
	}

	saveNewServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("ifGenerationMatch"); got != "0" {
			http.Error(w, "wrong generation", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"bucket":"bucket","name":"pairing/state.json","generation":"1"}`)
	}))
	defer saveNewServer.Close()
	saveNewClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(saveNewServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	saveNewBackend := &gcsBackend{client: saveNewClient, bucket: "bucket", object: "pairing/state.json"}
	if err := saveNewBackend.Save(context.Background(), newState(), 0, false); err != nil {
		t.Fatal(err)
	}

	saveErrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":400,"message":"boom"}}`, http.StatusBadRequest)
	}))
	defer saveErrorServer.Close()
	saveErrorClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(saveErrorServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	saveErrorBackend := &gcsBackend{client: saveErrorClient, bucket: "bucket", object: "pairing/state.json"}
	if err := saveErrorBackend.Save(context.Background(), newState(), 0, false); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("Save(generic server error) = %v, want non-conflict error", err)
	}
}

func TestGCSBackendNotFoundAndConflict(t *testing.T) {
	t.Parallel()

	notFoundServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
	}))
	defer notFoundServer.Close()
	notFoundClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(notFoundServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	notFoundBackend := &gcsBackend{client: notFoundClient, bucket: "bucket", object: "pairing/state.json"}
	st, generation, exists, err := notFoundBackend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exists || generation != 0 || st.Version != 1 {
		t.Fatalf("Load(not found) = gen %d exists %v state %+v", generation, exists, st)
	}

	conflictServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, `{"error":{"code":412,"message":"precondition failed"}}`, http.StatusPreconditionFailed)
	}))
	defer conflictServer.Close()
	conflictClient, err := gcs.NewClient(context.Background(), option.WithEndpoint(conflictServer.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	conflictBackend := &gcsBackend{client: conflictClient, bucket: "bucket", object: "pairing/state.json"}
	if err := conflictBackend.Save(context.Background(), newState(), 0, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save(precondition) error = %v, want conflict", err)
	}
}

func TestGCSHelpers(t *testing.T) {
	t.Parallel()

	if _, err := NewGCS(context.Background(), " ", "prefix"); err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("NewGCS(empty bucket) error = %v, want bucket error", err)
	}
	if !isGCSNotFound(gcs.ErrObjectNotExist) {
		t.Fatal("isGCSNotFound(storage.ErrObjectNotExist) = false, want true")
	}
	if !isGCSNotFound(&googleapi.Error{Code: httpStatusNotFound}) {
		t.Fatal("isGCSNotFound(404) = false, want true")
	}
	if isGCSNotFound(&googleapi.Error{Code: 500}) {
		t.Fatal("isGCSNotFound(500) = true, want false")
	}
	if !isGCSPreconditionFailed(&googleapi.Error{Code: httpStatusPreconditionFailed}) {
		t.Fatal("isGCSPreconditionFailed(412) = false, want true")
	}
	if isGCSPreconditionFailed(errors.New("boom")) {
		t.Fatal("isGCSPreconditionFailed(generic) = true, want false")
	}
}
