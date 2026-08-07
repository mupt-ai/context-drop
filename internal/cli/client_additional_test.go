package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUploadValidationAndRequest(t *testing.T) {
	if _, err := Upload(context.Background(), UploadRequest{ChainSessionToken: "session", Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("Upload(no endpoint) error = %v, want endpoint", err)
	}
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: "https://example.test", Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("Upload(no session) error = %v, want init error", err)
	}
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: "https://example.test", ChainSessionToken: "session"}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Upload(empty) error = %v, want empty", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/drops" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Filename"); got != "hello.txt" {
			t.Fatalf("X-Filename = %q", got)
		}
		if got := r.Header.Get("X-TTL"); got != "30m0s" {
			t.Fatalf("X-TTL = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"drop-1","url":"http://example.test/d/drop-1","expires_at":"2026-05-23T12:00:00Z","content_type":"text/plain","size":5}`)
	}))
	defer server.Close()

	resp, err := Upload(context.Background(), UploadRequest{
		Endpoint:          server.URL,
		ChainSessionToken: "session",
		Filename:          "hello.txt",
		ContentType:       "text/plain",
		TTL:               30 * time.Minute,
		Data:              []byte("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "drop-1" || resp.Size != 5 {
		t.Fatalf("Upload() = %+v", resp)
	}
}

func TestCreateChainInviteJoinAndMessagingClients(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chains":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"chain_id":"chain-1","machine_id":"mach-1","machine_name":"laptop","session_token":"session-1"}`)
		case "/v1/invites":
			if got := r.Header.Get("Authorization"); got != "Bearer session-1" {
				t.Fatalf("invite Authorization = %q", got)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"token":"join-token","expires_at":"2026-05-23T12:10:00Z","chain_id":"chain-1","machine_id":"mach-1","machine_name":"laptop"}`)
		case "/v1/join":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"chain_id":"chain-1","machine_id":"mach-2","machine_name":"desktop","session_token":"session-2"}`)
		case "/v1/machines":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"machines":[{"id":"mach-1","chain_id":"chain-1","name":"laptop","created_at":"2026-05-23T12:00:00Z","last_seen_at":"2026-05-23T12:01:00Z"}]}`)
		case "/v1/messages":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprint(w, `{"message":{"id":"msg-1","chain_id":"chain-1","from_machine_id":"mach-1","to_machine_id":"mach-2","body":"hello","created_at":"2026-05-23T12:02:00Z"}}`)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"messages":[{"id":"msg-1","chain_id":"chain-1","from_machine_id":"mach-2","to_machine_id":"mach-1","body":"hi","created_at":"2026-05-23T12:03:00Z"}]}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	chain, err := CreateChain(context.Background(), CreateChainRequest{Endpoint: server.URL, MachineName: "laptop"})
	if err != nil || chain.SessionToken != "session-1" {
		t.Fatalf("CreateChain() = %+v, %v", chain, err)
	}
	invite, err := CreateInvite(context.Background(), CreateInviteRequest{Endpoint: server.URL, ChainSessionToken: chain.SessionToken, TTL: time.Minute})
	if err != nil || invite.Token != "join-token" {
		t.Fatalf("CreateInvite() = %+v, %v", invite, err)
	}
	joined, err := JoinChain(context.Background(), JoinChainRequest{Endpoint: server.URL, Token: invite.Token, MachineName: "desktop"})
	if err != nil || joined.SessionToken != "session-2" {
		t.Fatalf("JoinChain() = %+v, %v", joined, err)
	}
	if machines, err := ListMachines(context.Background(), server.URL, chain.SessionToken); err != nil || len(machines.Machines) != 1 {
		t.Fatalf("ListMachines() = %+v, %v", machines, err)
	}
	if sent, err := SendMessage(context.Background(), server.URL, chain.SessionToken, "desktop", "hello"); err != nil || sent.Message.ID != "msg-1" {
		t.Fatalf("SendMessage() = %+v, %v", sent, err)
	}
	if messages, err := ListMessages(context.Background(), server.URL, chain.SessionToken); err != nil || len(messages.Messages) != 1 {
		t.Fatalf("ListMessages() = %+v, %v", messages, err)
	}
}

func TestClientErrorBranches(t *testing.T) {
	badEndpoint := "http://[::1"
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: badEndpoint, ChainSessionToken: "session", Data: []byte("x")}); err == nil {
		t.Fatal("Upload(bad endpoint) error = nil, want error")
	}
	if _, err := ListDrops(context.Background(), "https://example.test", ""); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("ListDrops(no session) error = %v", err)
	}
	if _, err := ListDrops(context.Background(), badEndpoint, "session"); err == nil {
		t.Fatal("ListDrops(bad endpoint) error = nil, want error")
	}
	if _, err := PullDrop(context.Background(), "https://example.test", "", "id"); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("PullDrop(no session) error = %v", err)
	}
	if _, err := PullDrop(context.Background(), badEndpoint, "session", "id"); err == nil {
		t.Fatal("PullDrop(bad endpoint) error = nil, want error")
	}
	if _, err := newJSONAPIRequest(context.Background(), http.MethodPost, "https://example.test", "/", "", make(chan int)); err == nil {
		t.Fatal("newJSONAPIRequest(marshal error) = nil, want error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/drops":
			w.WriteHeader(http.StatusTeapot)
			_, _ = fmt.Fprint(w, `{"msg":"bad drops"}`)
		case "/v1/drops/id/blob":
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error_description":"missing blob"}`)
		}
	}))
	defer server.Close()
	if _, err := Upload(context.Background(), UploadRequest{Endpoint: server.URL, ChainSessionToken: "session", Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "bad drops") {
		t.Fatalf("Upload(server error) = %v", err)
	}
	if _, err := ListDrops(context.Background(), server.URL, "session"); err == nil || !strings.Contains(err.Error(), "bad drops") {
		t.Fatalf("ListDrops(server error) = %v", err)
	}
	if _, err := PullDrop(context.Background(), server.URL, "session", "id"); err == nil || !strings.Contains(err.Error(), "missing blob") {
		t.Fatalf("PullDrop(server error) = %v", err)
	}

	for _, body := range []string{`{"error":"bad error"}`, `{"msg":"bad msg"}`, `{"error_description":"bad desc"}`, `{}`} {
		err := readError(&http.Response{Status: "499 status", Body: io.NopCloser(strings.NewReader(body))})
		if err == nil || err.Error() == "" {
			t.Fatalf("readError(%s) = %v", body, err)
		}
	}
	if got := filenameFromContentDisposition("not a header", "fallback"); got != "fallback" {
		t.Fatalf("filenameFromContentDisposition(fallback) = %q", got)
	}
}

func TestPairingClientErrorBranches(t *testing.T) {
	badEndpoint := "http://[::1"
	if _, err := CreateChain(context.Background(), CreateChainRequest{Endpoint: badEndpoint}); err == nil {
		t.Fatal("CreateChain(bad endpoint) error = nil")
	}
	if _, err := CreateInvite(context.Background(), CreateInviteRequest{Endpoint: badEndpoint, ChainSessionToken: "session"}); err == nil {
		t.Fatal("CreateInvite(bad endpoint) error = nil")
	}
	if _, err := JoinChain(context.Background(), JoinChainRequest{Endpoint: badEndpoint, Token: "join"}); err == nil {
		t.Fatal("JoinChain(bad endpoint) error = nil")
	}
	if _, err := ListMachines(context.Background(), badEndpoint, "session"); err == nil {
		t.Fatal("ListMachines(bad endpoint) error = nil")
	}
	if _, err := SendMessage(context.Background(), badEndpoint, "session", "machine", "hello"); err == nil {
		t.Fatal("SendMessage(bad endpoint) error = nil")
	}
	if _, err := ListMessages(context.Background(), badEndpoint, "session"); err == nil {
		t.Fatal("ListMessages(bad endpoint) error = nil")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"bad pairing"}`)
	}))
	defer server.Close()
	calls := []struct {
		name string
		run  func() error
	}{
		{name: "create chain", run: func() error {
			_, err := CreateChain(context.Background(), CreateChainRequest{Endpoint: server.URL})
			return err
		}},
		{name: "create invite", run: func() error {
			_, err := CreateInvite(context.Background(), CreateInviteRequest{Endpoint: server.URL, ChainSessionToken: "session"})
			return err
		}},
		{name: "join", run: func() error {
			_, err := JoinChain(context.Background(), JoinChainRequest{Endpoint: server.URL, Token: "join"})
			return err
		}},
		{name: "machines", run: func() error { _, err := ListMachines(context.Background(), server.URL, "session"); return err }},
		{name: "send", run: func() error {
			_, err := SendMessage(context.Background(), server.URL, "session", "machine", "hello")
			return err
		}},
		{name: "messages", run: func() error { _, err := ListMessages(context.Background(), server.URL, "session"); return err }},
	}
	for _, call := range calls {
		if err := call.run(); err == nil || !strings.Contains(err.Error(), "bad pairing") {
			t.Fatalf("%s error = %v, want bad pairing", call.name, err)
		}
	}
}

func TestListDropsAndPullDrop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer session" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/v1/drops":
			_, _ = fmt.Fprint(w, `{"drops":[{"id":"id","url":"http://example.test/d/id","filename":"a.txt","content_type":"text/plain","size":1,"created_at":"2026-05-23T12:00:00Z","expires_at":"2026-05-24T12:00:00Z"}]}`)
		case "/v1/drops/id/blob":
			w.Header().Set("Content-Disposition", `attachment; filename="a.txt"`)
			_, _ = fmt.Fprint(w, "x")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	list, err := ListDrops(context.Background(), server.URL, "session")
	if err != nil || len(list.Drops) != 1 || list.Drops[0].ID != "id" {
		t.Fatalf("ListDrops() = %+v, %v", list, err)
	}
	pulled, err := PullDrop(context.Background(), server.URL, "session", "id")
	if err != nil || pulled.Filename != "a.txt" || string(pulled.Data) != "x" {
		t.Fatalf("PullDrop() = %+v, %v", pulled, err)
	}
}
