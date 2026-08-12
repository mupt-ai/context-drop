package runtimeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"contextdrop.dev/context-drop/internal/localhome"
)

const DefaultAddress = "http://127.0.0.1:47762"

type ParentReport struct {
	ID                 string `json:"id"`
	RunID              string `json:"runId"`
	RouterID           string `json:"routerId"`
	ChatID             string `json:"chatId"`
	Kind               string `json:"kind,omitempty"`
	Message            string `json:"message"`
	SensitiveAction    string `json:"sensitiveAction,omitempty"`
	ChallengeToken     string `json:"challengeToken,omitempty"`
	ChallengedAction   string `json:"challengedAction,omitempty"`
	ChallengeExpiresAt string `json:"challengeExpiresAt,omitempty"`
	CreatedAt          string `json:"createdAt"`
	LeaseID            string `json:"leaseId,omitempty"`
}
type Agent struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	PromptMode string `json:"prompt_mode"`
}
type ManagedTask struct {
	RunID        string `json:"runId"`
	PaneID       string `json:"paneId"`
	Agent        string `json:"agent"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Selected     bool   `json:"selected"`
	FullyManaged bool   `json:"fullyManaged"`
}

type Run struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Agent          string `json:"agent"`
	Repo           string `json:"repo"`
	Backend        string `json:"backend"`
	TmuxSession    string `json:"tmuxSession,omitempty"`
	TmuxWindow     string `json:"tmuxWindow,omitempty"`
	TmuxPane       string `json:"tmuxPane,omitempty"`
	HerdrSession   string `json:"herdrSession,omitempty"`
	HerdrWorkspace string `json:"herdrWorkspace,omitempty"`
	HerdrTab       string `json:"herdrTab,omitempty"`
	HerdrPane      string `json:"herdrPane,omitempty"`
	Lane           string `json:"lane,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
}
type Client struct {
	Address, Token string
	HTTP           *http.Client
}

type HTTPError struct {
	StatusCode int
	Code       string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("runtime returned %s: %s", http.StatusText(e.StatusCode), e.Body)
}

func AutoAuthorizationFailureReason(err error) string {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	switch httpErr.Code {
	case "task_not_runnable", "authorization_expired":
		return httpErr.Code
	default:
		return ""
	}
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
		body := string(bytes.TrimSpace(b))
		var payload struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &payload)
		return &HTTPError{StatusCode: resp.StatusCode, Code: payload.Code, Body: body}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, &map[string]any{}, http.StatusOK)
}
func (c *Client) IssueRouterCapability(ctx context.Context, routerID, chatID string) (string, error) {
	var out struct {
		Capability string `json:"capability"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/router-capabilities", map[string]string{"routerId": routerID, "chatId": chatID}, &out, http.StatusCreated)
	return out.Capability, err
}
func (c *Client) LeaseReport(ctx context.Context, routerID, chatID string) (ParentReport, bool, error) {
	var out struct {
		Report ParentReport `json:"report"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/reports/lease", map[string]string{"routerId": routerID, "chatId": chatID}, &out, http.StatusOK)
	if err != nil {
		return ParentReport{}, false, err
	}
	return out.Report, out.Report.ID != "", nil
}
func (c *Client) FinishReport(ctx context.Context, report ParentReport, routerID, chatID string, delivered bool) error {
	action := "release"
	if delivered {
		action = "ack"
	}
	return c.do(ctx, http.MethodPost, "/v1/reports/"+url.PathEscape(report.ID)+"/"+action, map[string]string{"routerId": routerID, "chatId": chatID, "leaseId": report.LeaseID}, &map[string]any{}, http.StatusOK)
}
func (c *Client) AutoAuthorize(ctx context.Context, report ParentReport, routerID, chatID string) (Run, string, error) {
	var out struct {
		Run     Run    `json:"run"`
		Outcome string `json:"outcome"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/reports/"+url.PathEscape(report.ID)+"/auto-authorize", map[string]string{"routerId": routerID, "chatId": chatID, "leaseId": report.LeaseID}, &out, http.StatusCreated)
	return out.Run, out.Outcome, err
}
func (c *Client) Confirm(ctx context.Context, routerID, chatID, token string) (Run, error) {
	var out struct {
		Run Run `json:"run"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/confirm", map[string]string{"routerId": routerID, "chatId": chatID, "token": token}, &out, http.StatusCreated)
	return out.Run, err
}
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var out struct {
		Agents []Agent `json:"agents"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/agents", nil, &out, http.StatusOK)
	return out.Agents, err
}
func (c *Client) LaunchManagedSchedule(ctx context.Context, agent, repo, prompt, name, backend, routerID, chatID string) (ManagedTask, error) {
	var out struct {
		RunID string      `json:"runId"`
		Task  ManagedTask `json:"task"`
	}
	request := map[string]string{"agent": agent, "repo": repo, "prompt": prompt, "name": name, "routerId": routerID, "chatId": chatID}
	if backend != "" {
		request["backend"] = backend
	}
	err := c.do(ctx, http.MethodPost, "/v1/tasks/schedule", request, &out, http.StatusCreated)
	out.Task.RunID = out.RunID
	return out.Task, err
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
