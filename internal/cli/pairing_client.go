package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type CreateChainRequest struct {
	Endpoint    string
	MachineName string
}

type CreateChainResponse struct {
	ChainID      string `json:"chain_id"`
	MachineID    string `json:"machine_id"`
	MachineName  string `json:"machine_name"`
	SessionToken string `json:"session_token"`
}

type CreateInviteRequest struct {
	Endpoint          string
	ChainSessionToken string
	TTL               time.Duration
}

type CreateInviteResponse struct {
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ChainID      string    `json:"chain_id"`
	MachineID    string    `json:"machine_id"`
	MachineName  string    `json:"machine_name"`
	SessionToken string    `json:"session_token"`
}

type JoinChainRequest struct {
	Endpoint    string
	Token       string
	MachineName string
}

type JoinChainResponse struct {
	ChainID      string `json:"chain_id"`
	MachineID    string `json:"machine_id"`
	MachineName  string `json:"machine_name"`
	SessionToken string `json:"session_token"`
}

type MachineSummary struct {
	ID         string    `json:"id"`
	ChainID    string    `json:"chain_id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type ListMachinesResponse struct {
	Machines []MachineSummary `json:"machines"`
}

type MessageSummary struct {
	ID            string    `json:"id"`
	ChainID       string    `json:"chain_id"`
	FromMachineID string    `json:"from_machine_id"`
	ToMachineID   string    `json:"to_machine_id"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

type SendMessageResponse struct {
	Message MessageSummary `json:"message"`
}

type ListMessagesResponse struct {
	Messages []MessageSummary `json:"messages"`
}

func CreateChain(ctx context.Context, req CreateChainRequest) (CreateChainResponse, error) {
	if req.Endpoint == "" {
		return CreateChainResponse{}, fmt.Errorf("endpoint is required")
	}
	httpReq, err := newJSONAPIRequest(ctx, http.MethodPost, req.Endpoint, "/v1/chains", "", map[string]string{
		"machine_name": req.MachineName,
	})
	if err != nil {
		return CreateChainResponse{}, err
	}
	var out CreateChainResponse
	if err := doJSON(httpReq, http.StatusCreated, &out); err != nil {
		return CreateChainResponse{}, fmt.Errorf("init failed: %w", err)
	}
	return out, nil
}

func CreateInvite(ctx context.Context, req CreateInviteRequest) (CreateInviteResponse, error) {
	if req.Endpoint == "" {
		return CreateInviteResponse{}, fmt.Errorf("endpoint is required")
	}
	bearer := strings.TrimSpace(req.ChainSessionToken)
	if bearer == "" {
		return CreateInviteResponse{}, errNotInitialized()
	}

	body := map[string]string{}
	if req.TTL > 0 {
		body["ttl"] = req.TTL.String()
	}
	httpReq, err := newJSONAPIRequest(ctx, http.MethodPost, req.Endpoint, "/v1/invites", bearer, body)
	if err != nil {
		return CreateInviteResponse{}, err
	}
	var out CreateInviteResponse
	if err := doJSON(httpReq, http.StatusCreated, &out); err != nil {
		return CreateInviteResponse{}, fmt.Errorf("create join token failed: %w", err)
	}
	return out, nil
}

func JoinChain(ctx context.Context, req JoinChainRequest) (JoinChainResponse, error) {
	if req.Endpoint == "" {
		return JoinChainResponse{}, fmt.Errorf("endpoint is required")
	}
	if strings.TrimSpace(req.Token) == "" {
		return JoinChainResponse{}, fmt.Errorf("join token is required")
	}
	body := map[string]string{"token": req.Token, "machine_name": req.MachineName}
	httpReq, err := newJSONAPIRequest(ctx, http.MethodPost, req.Endpoint, "/v1/join", "", body)
	if err != nil {
		return JoinChainResponse{}, err
	}
	var out JoinChainResponse
	if err := doJSON(httpReq, http.StatusCreated, &out); err != nil {
		return JoinChainResponse{}, fmt.Errorf("join failed: %w", err)
	}
	return out, nil
}

func ListMachines(ctx context.Context, endpoint, chainSessionToken string) (ListMachinesResponse, error) {
	if chainSessionToken == "" {
		return ListMachinesResponse{}, errNotInitialized()
	}
	req, err := newAPIRequest(ctx, http.MethodGet, endpoint, "/v1/machines", chainSessionToken, nil)
	if err != nil {
		return ListMachinesResponse{}, err
	}
	var out ListMachinesResponse
	if err := doJSON(req, http.StatusOK, &out); err != nil {
		return ListMachinesResponse{}, fmt.Errorf("machines list failed: %w", err)
	}
	return out, nil
}

func SendMessage(ctx context.Context, endpoint, chainSessionToken, to, body string) (SendMessageResponse, error) {
	if chainSessionToken == "" {
		return SendMessageResponse{}, errNotInitialized()
	}
	req, err := newJSONAPIRequest(ctx, http.MethodPost, endpoint, "/v1/messages", chainSessionToken, map[string]string{
		"to":   to,
		"body": body,
	})
	if err != nil {
		return SendMessageResponse{}, err
	}
	var out SendMessageResponse
	if err := doJSON(req, http.StatusCreated, &out); err != nil {
		return SendMessageResponse{}, fmt.Errorf("send failed: %w", err)
	}
	return out, nil
}

func ListMessages(ctx context.Context, endpoint, chainSessionToken string) (ListMessagesResponse, error) {
	if chainSessionToken == "" {
		return ListMessagesResponse{}, errNotInitialized()
	}
	req, err := newAPIRequest(ctx, http.MethodGet, endpoint, "/v1/messages", chainSessionToken, nil)
	if err != nil {
		return ListMessagesResponse{}, err
	}
	var out ListMessagesResponse
	if err := doJSON(req, http.StatusOK, &out); err != nil {
		return ListMessagesResponse{}, fmt.Errorf("messages list failed: %w", err)
	}
	return out, nil
}
