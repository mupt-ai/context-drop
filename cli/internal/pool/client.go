package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultServer is hosted Context Drop.
const DefaultServer = "https://contextdrop.dev"

// Membership is this machine's place in an org, saved by `join`.
type Membership struct {
	Server     string `json:"server"`
	Org        string `json:"org"`
	Machine    string `json:"machine"`
	Credential string `json:"credential"`
}

// MembershipPath is where the membership lives under the Context Drop home.
func MembershipPath(home string) string { return filepath.Join(home, "org.json") }

// ErrNotJoined means this machine has not joined an org.
var ErrNotJoined = errors.New("this machine has not joined an org; run `context-drop join <token>`")

// LoadMembership reads the saved membership.
func LoadMembership(home string) (Membership, error) {
	var m Membership
	raw, err := os.ReadFile(MembershipPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrNotJoined
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("read %s: %w", MembershipPath(home), err)
	}
	if m.Server == "" || m.Org == "" || m.Machine == "" || m.Credential == "" {
		return m, fmt.Errorf("%s is incomplete; run `context-drop join <token>` again", MembershipPath(home))
	}
	return m, nil
}

// SaveMembership writes the membership readable only by this user.
func SaveMembership(home string, m Membership) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := MembershipPath(home)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// JoinResponse is what the server returns to `join`.
type JoinResponse struct {
	Org        string `json:"org"`
	Machine    string `json:"machine"`
	Credential string `json:"credential"`
}

// Join exchanges an org's join token for a machine credential.
func Join(ctx context.Context, client *http.Client, server, token, machine string) (Membership, error) {
	server = strings.TrimRight(server, "/")
	var out JoinResponse
	err := do(ctx, client, server+"/v1/join", "", map[string]string{"token": token, "machine": machine}, &out)
	if err != nil {
		return Membership{}, err
	}
	return Membership{Server: server, Org: out.Org, Machine: out.Machine, Credential: out.Credential}, nil
}

// Client calls the org's endpoints as this machine.
type Client struct {
	Membership
	HTTP *http.Client
}

func (c Client) call(ctx context.Context, name string, in, out any) error {
	return do(ctx, c.HTTP, c.Server+"/v1/"+c.Org+"/"+name, c.Credential, in, out)
}

// List returns the org's agents that match sel.
func (c Client) List(ctx context.Context, sel Selector) ([]Agent, error) {
	var out []Agent
	return out, c.call(ctx, "list", sel, &out)
}

// Read returns an agent's transcript.
func (c Client) Read(ctx context.Context, id string, opts ReadOptions) (ReadResult, error) {
	var out ReadResult
	return out, c.call(ctx, "read", struct {
		ID string `json:"id"`
		ReadOptions
	}{id, opts}, &out)
}

// Send delivers a message to the selected agents.
func (c Client) Send(ctx context.Context, req SendRequest) (SendResponse, error) {
	var out SendResponse
	return out, c.call(ctx, "send", req, &out)
}

// Ask answers a question from a copy of one agent's session, without interrupting it. A copy can take
// minutes to answer, so it waits as long as Post.
func (c Client) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	var out AskResult
	return out, c.Post(ctx, "ask", req, &out)
}

// Post calls any org endpoint by path, such as "mods/deploy" or "call/standup". Mods can run for up
// to 15 minutes, so it waits longer than the other calls.
func (c Client) Post(ctx context.Context, path string, in, out any) error {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 16 * time.Minute}
	}
	return do(ctx, client, c.Server+"/v1/"+c.Org+"/"+path, c.Credential, in, out)
}

// Leave removes this machine from the org.
func (c Client) Leave(ctx context.Context) error {
	return c.call(ctx, "leave", struct{}{}, nil)
}

func do(ctx context.Context, client *http.Client, url, credential string, in, out any) error {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error *Error `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return e.Error
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
