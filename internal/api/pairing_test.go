package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/pairing"
	"contextdrop.dev/context-drop/internal/storage"
)

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (failingBody) Close() error {
	return nil
}

type fakePairingStore struct {
	createChain   func(context.Context, string, time.Time) (pairing.Principal, string, pairing.Machine, error)
	createInvite  func(context.Context, pairing.Principal, time.Duration, time.Time) (string, pairing.Invite, error)
	consumeInvite func(context.Context, string, string, time.Time) (pairing.Principal, string, pairing.Machine, error)
	authenticate  func(context.Context, string, time.Time) (pairing.Principal, bool, error)
	listMachines  func(context.Context, pairing.Principal) ([]pairing.Machine, error)
	sendMessage   func(context.Context, pairing.Principal, string, string, time.Time) (pairing.Message, error)
	listMessages  func(context.Context, pairing.Principal) ([]pairing.Message, error)
}

func (s fakePairingStore) CreateChain(ctx context.Context, machineName string, now time.Time) (pairing.Principal, string, pairing.Machine, error) {
	return s.createChain(ctx, machineName, now)
}

func (s fakePairingStore) CreateInvite(ctx context.Context, principal pairing.Principal, ttl time.Duration, now time.Time) (string, pairing.Invite, error) {
	return s.createInvite(ctx, principal, ttl, now)
}

func (s fakePairingStore) ConsumeInvite(ctx context.Context, token, machineName string, now time.Time) (pairing.Principal, string, pairing.Machine, error) {
	return s.consumeInvite(ctx, token, machineName, now)
}

func (s fakePairingStore) AuthenticateSession(ctx context.Context, token string, now time.Time) (pairing.Principal, bool, error) {
	return s.authenticate(ctx, token, now)
}

func (s fakePairingStore) ListMachines(ctx context.Context, principal pairing.Principal) ([]pairing.Machine, error) {
	return s.listMachines(ctx, principal)
}

func (s fakePairingStore) SendMessage(ctx context.Context, principal pairing.Principal, to, body string, now time.Time) (pairing.Message, error) {
	return s.sendMessage(ctx, principal, to, body, now)
}

func (s fakePairingStore) ListMessages(ctx context.Context, principal pairing.Principal) ([]pairing.Message, error) {
	return s.listMessages(ctx, principal)
}

func newPairingTestServer(t *testing.T, joinTokenTTL time.Duration) *Server {
	t.Helper()
	return NewServer(Options{
		BaseURL:      "http://example.test",
		Store:        storage.NewLocal(t.TempDir()),
		PairingStore: pairing.NewMemory(),
		DefaultTTL:   time.Hour,
		JoinTokenTTL: joinTokenTTL,
		MaxTTL:       24 * time.Hour,
		MaxBytes:     1024,
	})
}

func TestServer_PairingTokenSingleUseAndMessagesScopedToChain(t *testing.T) {
	t.Parallel()

	server := newPairingTestServer(t, time.Minute)
	h := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/invites", strings.NewReader(`{"machine_name":"owner"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated invite status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	owner := createChainForTest(t, h, "owner")
	ownerInvite := createInviteForTest(t, h, owner.SessionToken, "owner")
	joined := joinForTest(t, h, ownerInvite.Token, "laptop")
	if owner.MachineID == joined.MachineID {
		t.Fatalf("joined machine ID reused owner machine ID %q", joined.MachineID)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/join", strings.NewReader(`{"token":"`+ownerInvite.Token+`","machine_name":"reuse"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused join token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+joined.SessionToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("machines status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var machines listMachinesResponse
	if err := json.NewDecoder(rec.Body).Decode(&machines); err != nil {
		t.Fatal(err)
	}
	if len(machines.Machines) != 2 {
		t.Fatalf("machine count = %d, want 2", len(machines.Machines))
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"to":"`+joined.MachineID+`","body":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+owner.SessionToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+joined.SessionToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var messages listMessagesResponse
	if err := json.NewDecoder(rec.Body).Decode(&messages); err != nil {
		t.Fatal(err)
	}
	if len(messages.Messages) != 1 || messages.Messages[0].Body != "hello" || messages.Messages[0].ToMachineID != joined.MachineID {
		t.Fatalf("messages = %+v, want one scoped message", messages.Messages)
	}

	other := createChainForTest(t, h, "other")
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"to":"`+joined.MachineID+`","body":"cross-chain"}`))
	req.Header.Set("Authorization", "Bearer "+other.SessionToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-chain send status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_ExpiredPairingTokenCannotJoin(t *testing.T) {
	t.Parallel()

	server := newPairingTestServer(t, time.Nanosecond)
	h := server.Handler()

	owner := createChainForTest(t, h, "owner")
	invite := createInviteForTest(t, h, owner.SessionToken, "owner")
	time.Sleep(time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/v1/join", strings.NewReader(`{"token":"`+invite.Token+`","machine_name":"late"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServer_CreateInviteWithChainSessionAndBadInputs(t *testing.T) {
	t.Parallel()

	server := newPairingTestServer(t, time.Minute)
	h := server.Handler()
	owner := createChainForTest(t, h, "owner")

	req := httptest.NewRequest(http.MethodPost, "/v1/invites", strings.NewReader(`{"ttl":"30s"}`))
	req.Header.Set("Authorization", "Bearer "+owner.SessionToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("chain-session invite status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var chained createInviteResponse
	if err := json.NewDecoder(rec.Body).Decode(&chained); err != nil {
		t.Fatal(err)
	}
	if chained.SessionToken != "" || chained.MachineID != owner.MachineID || chained.MachineName != "owner" {
		t.Fatalf("chain-session invite = %+v, want existing machine without new session token", chained)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/invites", strings.NewReader(`{"ttl":"bad"}`))
	req.Header.Set("Authorization", "Bearer "+owner.SessionToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ttl status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "init invalid json", method: http.MethodPost, path: "/v1/chains", body: `{"machine_name"`},
		{name: "join invalid json", method: http.MethodPost, path: "/v1/join", body: `{"token"`},
		{name: "invite invalid json", method: http.MethodPost, path: "/v1/invites", body: `{"ttl"`},
		{name: "send invalid json", method: http.MethodPost, path: "/v1/messages", body: `{"to"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+owner.SessionToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}

	for _, path := range []string{"/v1/machines", "/v1/messages"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status = %d, want %d", path, rec.Code, http.StatusUnauthorized)
		}
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"to":"machine-1","body":"hello"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated send status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServer_PairingStoreErrors(t *testing.T) {
	t.Parallel()

	principal := pairing.Principal{ChainID: "chain-1", MachineID: "machine-1"}
	machine := pairing.Machine{ID: "machine-1", ChainID: "chain-1", Name: "owner"}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		store  fakePairingStore
		bearer string
		want   int
	}{
		{
			name:   "create chain error",
			method: http.MethodPost,
			path:   "/v1/chains",
			body:   `{}`,
			want:   http.StatusConflict,
			store: fakePairingStore{
				createChain: func(context.Context, string, time.Time) (pairing.Principal, string, pairing.Machine, error) {
					return pairing.Principal{}, "", pairing.Machine{}, pairing.ErrConflict
				},
			},
		},
		{
			name:   "create invite machine load error",
			method: http.MethodPost,
			path:   "/v1/invites",
			body:   `{}`,
			bearer: "chain-session",
			want:   http.StatusInternalServerError,
			store: fakePairingStore{
				authenticate: func(context.Context, string, time.Time) (pairing.Principal, bool, error) {
					return principal, true, nil
				},
				listMachines: func(context.Context, pairing.Principal) ([]pairing.Machine, error) {
					return nil, pairing.ErrMachineNotFound
				},
			},
		},
		{
			name:   "create invite error",
			method: http.MethodPost,
			path:   "/v1/invites",
			body:   `{}`,
			bearer: "chain-session",
			want:   http.StatusBadRequest,
			store: fakePairingStore{
				authenticate: func(context.Context, string, time.Time) (pairing.Principal, bool, error) {
					return principal, true, nil
				},
				listMachines: func(context.Context, pairing.Principal) ([]pairing.Machine, error) {
					return []pairing.Machine{machine}, nil
				},
				createInvite: func(context.Context, pairing.Principal, time.Duration, time.Time) (string, pairing.Invite, error) {
					return "", pairing.Invite{}, pairing.ErrMessageEmpty
				},
			},
		},
		{
			name:   "session auth store error",
			method: http.MethodGet,
			path:   "/v1/machines",
			bearer: "chain-session",
			want:   http.StatusInternalServerError,
			store: fakePairingStore{
				authenticate: func(context.Context, string, time.Time) (pairing.Principal, bool, error) {
					return pairing.Principal{}, false, errors.New("auth store down")
				},
			},
		},
		{
			name:   "list machines error",
			method: http.MethodGet,
			path:   "/v1/machines",
			bearer: "chain-session",
			want:   http.StatusConflict,
			store: fakePairingStore{
				authenticate: func(context.Context, string, time.Time) (pairing.Principal, bool, error) {
					return principal, true, nil
				},
				listMachines: func(context.Context, pairing.Principal) ([]pairing.Machine, error) {
					return nil, pairing.ErrConflict
				},
			},
		},
		{
			name:   "send message error",
			method: http.MethodPost,
			path:   "/v1/messages",
			body:   `{"to":"machine-2","body":"hello"}`,
			bearer: "chain-session",
			want:   http.StatusBadRequest,
			store: fakePairingStore{
				authenticate: func(context.Context, string, time.Time) (pairing.Principal, bool, error) {
					return principal, true, nil
				},
				sendMessage: func(context.Context, pairing.Principal, string, string, time.Time) (pairing.Message, error) {
					return pairing.Message{}, pairing.ErrAmbiguousMachine
				},
			},
		},
		{
			name:   "list messages error",
			method: http.MethodGet,
			path:   "/v1/messages",
			bearer: "chain-session",
			want:   http.StatusConflict,
			store: fakePairingStore{
				authenticate: func(context.Context, string, time.Time) (pairing.Principal, bool, error) {
					return principal, true, nil
				},
				listMessages: func(context.Context, pairing.Principal) ([]pairing.Message, error) {
					return nil, pairing.ErrConflict
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(Options{
				BaseURL:      "http://example.test",
				Store:        storage.NewLocal(t.TempDir()),
				PairingStore: tc.store,
				DefaultTTL:   time.Hour,
				JoinTokenTTL: time.Minute,
				MaxTTL:       24 * time.Hour,
				MaxBytes:     1024,
			})
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestPairingResponseHelpers(t *testing.T) {
	t.Parallel()

	fallback := 2 * time.Minute
	if ttl, err := inviteTTL("", fallback); err != nil || ttl != fallback {
		t.Fatalf("inviteTTL(empty) = %s, %v; want fallback", ttl, err)
	}
	if _, err := inviteTTL("not-duration", fallback); err == nil || !strings.Contains(err.Error(), "invalid ttl") {
		t.Fatalf("inviteTTL(invalid) error = %v, want invalid ttl", err)
	}

	var req createInviteRequest
	nilBodyReq := httptest.NewRequest(http.MethodPost, "/", nil)
	nilBodyReq.Body = nil
	if err := decodeOptionalJSON(nilBodyReq, &req); err != nil {
		t.Fatalf("decodeOptionalJSON(nil body) error = %v", err)
	}
	for _, body := range []string{"", "   "} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if err := decodeOptionalJSON(r, &req); err != nil {
			t.Fatalf("decodeOptionalJSON(%q) error = %v", body, err)
		}
	}
	failingReq := httptest.NewRequest(http.MethodPost, "/", nil)
	failingReq.Body = failingBody{}
	if err := decodeOptionalJSON(failingReq, &req); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("decodeOptionalJSON(read error) = %v, want read failed", err)
	}
	large := strings.Repeat("x", 64*1024+1)
	if err := decodeOptionalJSON(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(large)), &req); err == nil {
		t.Fatal("decodeOptionalJSON(large) error = nil, want error")
	}

	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{name: "ambiguous", err: pairing.ErrAmbiguousMachine, status: http.StatusBadRequest, body: "ambiguous"},
		{name: "empty", err: pairing.ErrMessageEmpty, status: http.StatusBadRequest, body: "message is empty"},
		{name: "conflict", err: pairing.ErrConflict, status: http.StatusConflict, body: "conflict"},
		{name: "ttl", err: errors.New("join token ttl must be positive"), status: http.StatusBadRequest, body: "ttl"},
		{name: "too long", err: errors.New("message exceeds 4096 characters"), status: http.StatusBadRequest, body: "message exceeds"},
		{name: "unknown", err: errors.New("boom"), status: http.StatusInternalServerError, body: "pairing operation failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writePairingError(rec, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tc.body)
			}
		})
	}
}

func createChainForTest(t *testing.T, h http.Handler, machineName string) createChainResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chains", strings.NewReader(`{"machine_name":"`+machineName+`"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create chain status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out createChainResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionToken == "" || out.ChainID == "" || out.MachineID == "" {
		t.Fatalf("create chain response missing fields: %+v", out)
	}
	return out
}

func createInviteForTest(t *testing.T, h http.Handler, bearer string, machineName string) createInviteResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/invites", strings.NewReader(`{"machine_name":"`+machineName+`"}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invite status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out createInviteResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" || out.ChainID == "" || out.MachineID == "" {
		t.Fatalf("invite response missing fields: %+v", out)
	}
	return out
}

func joinForTest(t *testing.T, h http.Handler, token string, machineName string) joinResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/join", strings.NewReader(`{"token":"`+token+`","machine_name":"`+machineName+`"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("join status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out joinResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionToken == "" || out.ChainID == "" || out.MachineID == "" {
		t.Fatalf("join response missing fields: %+v", out)
	}
	return out
}
