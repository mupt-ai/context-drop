package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"contextdrop.dev/context-drop/internal/handoff"
)

type CreateHandoffRequest struct {
	Endpoint, ChainSessionToken, To, Summary, RequestedAction, Repository, BaseCommit, SourceRunID, TTL string
	Artifacts                                                                                           []handoff.Artifact
}

type ListHandoffsResponse struct {
	Handoffs []handoff.Handoff `json:"handoffs"`
}

func CreateHandoff(ctx context.Context, in CreateHandoffRequest) (handoff.Handoff, error) {
	body := map[string]any{"to": in.To, "summary": in.Summary, "requested_action": in.RequestedAction,
		"repository": in.Repository, "base_commit": in.BaseCommit, "source_run_id": in.SourceRunID,
		"ttl": in.TTL, "artifacts": in.Artifacts}
	req, err := newJSONAPIRequest(ctx, http.MethodPost, in.Endpoint, "/v1/handoffs", in.ChainSessionToken, body)
	if err != nil {
		return handoff.Handoff{}, err
	}
	var out handoff.Handoff
	if err := doJSON(req, http.StatusCreated, &out); err != nil {
		return out, fmt.Errorf("create handoff: %w", err)
	}
	return out, nil
}

func ListHandoffs(ctx context.Context, endpoint, token string) (ListHandoffsResponse, error) {
	req, err := newAPIRequest(ctx, http.MethodGet, endpoint, "/v1/handoffs", token, nil)
	if err != nil {
		return ListHandoffsResponse{}, err
	}
	var out ListHandoffsResponse
	if err := doJSON(req, http.StatusOK, &out); err != nil {
		return out, fmt.Errorf("list inbox: %w", err)
	}
	return out, nil
}

func GetHandoff(ctx context.Context, endpoint, token, id string) (handoff.Handoff, error) {
	req, err := newAPIRequest(ctx, http.MethodGet, endpoint, "/v1/handoffs/"+url.PathEscape(id), token, nil)
	if err != nil {
		return handoff.Handoff{}, err
	}
	var out handoff.Handoff
	if err := doJSON(req, http.StatusOK, &out); err != nil {
		return out, fmt.Errorf("get handoff: %w", err)
	}
	return out, nil
}

func SetHandoffState(ctx context.Context, endpoint, token, id string, state handoff.State) (handoff.Handoff, error) {
	req, err := newJSONAPIRequest(ctx, http.MethodPost, endpoint, "/v1/handoffs/"+url.PathEscape(id)+"/state", token, map[string]any{"state": state})
	if err != nil {
		return handoff.Handoff{}, err
	}
	var out handoff.Handoff
	if err := doJSON(req, http.StatusOK, &out); err != nil {
		return out, fmt.Errorf("update handoff: %w", err)
	}
	return out, nil
}
