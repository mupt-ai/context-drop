package runtimeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/localhome"
)

const DefaultAddress = "http://127.0.0.1:47762"

type ParentReport struct {
	ID        string `json:"id"`
	RunID     string `json:"runId"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
}
type Delegation struct {
	ID        string `json:"id"`
	RunID     string `json:"runId"`
	ChatID    string `json:"chatID"`
	Task      string `json:"task"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}
type Agent struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	PromptMode string `json:"prompt_mode"`
}
type Run struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Agent          string `json:"agent"`
	Repo           string `json:"repo"`
	Backend        string `json:"backend"`
	TmuxSession    string `json:"tmuxSession,omitempty"`
	TmuxWindow     string `json:"tmuxWindow,omitempty"`
	HerdrSession   string `json:"herdrSession,omitempty"`
	HerdrWorkspace string `json:"herdrWorkspace,omitempty"`
	HerdrTab       string `json:"herdrTab,omitempty"`
	HerdrPane      string `json:"herdrPane,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
}
type Client struct {
	Address, Token string
	HTTP           *http.Client
}

func Paths() (dir, configPath, tokenPath string, err error) {
	base, err := localhome.Root()
	if err != nil {
		return "", "", "", err
	}
	dir = filepath.Join(base, "runtime")
	return dir, filepath.Join(dir, "config.json"), filepath.Join(dir, "token"), nil
}
func New() (*Client, error) {
	_, _, token, err := Paths()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(token)
	if err != nil {
		return nil, fmt.Errorf("runtime not initialized; run context-drop init: %w", err)
	}
	address := os.Getenv("CONTEXT_DROP_RUNTIME_ADDRESS")
	if address == "" {
		cfg, configErr := LoadConfig()
		if configErr == nil {
			address = fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
		} else {
			address = DefaultAddress
		}
	}
	return &Client{Address: address, Token: string(bytes.TrimSpace(b)), HTTP: &http.Client{Timeout: 10 * time.Second}}, nil
}
func (c *Client) do(ctx context.Context, method, path string, in, out any, status int) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Address+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("local runtime unavailable; start it with context-drop runtime serve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("runtime returned %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, &map[string]any{}, http.StatusOK)
}
func (c *Client) Delegate(ctx context.Context, task, chatID string) (Run, error) {
	var out struct {
		Run Run `json:"run"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/delegate", map[string]string{"task": task, "chatID": chatID}, &out, http.StatusCreated)
	return out.Run, err
}
func (c *Client) DelegateCapability(ctx context.Context) (string, error) {
	var out struct {
		Capability string `json:"capability"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/delegate-capability", nil, &out, http.StatusOK)
	return out.Capability, err
}
func (c *Client) PendingReports(ctx context.Context) ([]ParentReport, error) {
	var out struct {
		Reports []ParentReport `json:"reports"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/reports", nil, &out, http.StatusOK)
	return out.Reports, err
}
func (c *Client) DeliverReport(ctx context.Context, id string) (ParentReport, bool, error) {
	var out struct {
		Report ParentReport `json:"report"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/reports/"+url.PathEscape(id)+"/deliver", map[string]string{}, &out, http.StatusOK)
	if err != nil && strings.Contains(err.Error(), "already delivered") {
		return ParentReport{}, false, nil
	}
	return out.Report, err == nil, err
}
func (c *Client) Delegations(ctx context.Context) ([]Delegation, error) {
	var out struct {
		Tasks []Delegation `json:"tasks"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/delegations", nil, &out, http.StatusOK)
	return out.Tasks, err
}
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var out struct {
		Agents []Agent `json:"agents"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/agents", nil, &out, http.StatusOK)
	return out.Agents, err
}
func (c *Client) Launch(ctx context.Context, agent, repo, prompt, name, backend, workspace string) (Run, error) {
	var out Run
	request := map[string]string{"agent": agent, "repo": repo, "prompt": prompt, "name": name}
	if backend != "" {
		request["backend"] = backend
	}
	if workspace != "" {
		request["workspaceId"] = workspace
	}
	err := c.do(ctx, http.MethodPost, "/v1/runs", request, &out, http.StatusCreated)
	return out, err
}
func (c *Client) Runs(ctx context.Context) ([]Run, error) {
	var out struct {
		Runs []Run `json:"runs"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/runs", nil, &out, http.StatusOK)
	return out.Runs, err
}
func (c *Client) Run(ctx context.Context, id string) (Run, error) {
	var out Run
	err := c.do(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, &out, http.StatusOK)
	return out, err
}
