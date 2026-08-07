package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type UploadRequest struct {
	Endpoint          string
	ChainSessionToken string
	Filename          string
	ContentType       string
	TTL               time.Duration
	Data              []byte
}

type UploadResponse struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expires_at"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
}

type ListResponse struct {
	Drops []DropSummary `json:"drops"`
}

type DropSummary struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type PullResponse struct {
	Filename    string
	ContentType string
	Data        []byte
}

func Upload(ctx context.Context, req UploadRequest) (UploadResponse, error) {
	if req.Endpoint == "" {
		return UploadResponse{}, fmt.Errorf("endpoint is required")
	}
	if req.ChainSessionToken == "" {
		return UploadResponse{}, errNotInitialized()
	}
	if len(req.Data) == 0 {
		return UploadResponse{}, fmt.Errorf("upload data is empty")
	}

	httpReq, err := newAPIRequest(ctx, http.MethodPost, req.Endpoint, "/v1/drops", req.ChainSessionToken, bytes.NewReader(req.Data))
	if err != nil {
		return UploadResponse{}, err
	}
	httpReq.Header.Set("X-Filename", req.Filename)
	if req.ContentType != "" {
		httpReq.Header.Set("Content-Type", req.ContentType)
	}
	if req.TTL > 0 {
		httpReq.Header.Set("X-TTL", req.TTL.String())
	}

	var out UploadResponse
	if err := doJSON(httpReq, http.StatusCreated, &out); err != nil {
		return UploadResponse{}, fmt.Errorf("upload failed: %w", err)
	}
	return out, nil
}

func ListDrops(ctx context.Context, endpoint, chainSessionToken string) (ListResponse, error) {
	if chainSessionToken == "" {
		return ListResponse{}, errNotInitialized()
	}
	req, err := newAPIRequest(ctx, http.MethodGet, endpoint, "/v1/drops", chainSessionToken, nil)
	if err != nil {
		return ListResponse{}, err
	}
	var out ListResponse
	if err := doJSON(req, http.StatusOK, &out); err != nil {
		return ListResponse{}, fmt.Errorf("list failed: %w", err)
	}
	return out, nil
}

func PullDrop(ctx context.Context, endpoint, chainSessionToken, id string) (PullResponse, error) {
	if chainSessionToken == "" {
		return PullResponse{}, errNotInitialized()
	}
	req, err := newAPIRequest(ctx, http.MethodGet, endpoint, "/v1/drops/"+id+"/blob", chainSessionToken, nil)
	if err != nil {
		return PullResponse{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return PullResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PullResponse{}, readError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return PullResponse{}, err
	}
	return PullResponse{
		Filename:    filenameFromContentDisposition(resp.Header.Get("Content-Disposition"), id),
		ContentType: resp.Header.Get("Content-Type"),
		Data:        data,
	}, nil
}

func newAPIRequest(ctx context.Context, method, endpoint, path, bearer string, body io.Reader) (*http.Request, error) {
	url := strings.TrimRight(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	return req, nil
}

func newJSONAPIRequest(ctx context.Context, method, endpoint, path, bearer string, body any) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func doJSON(req *http.Request, wantStatus int, out any) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func readError(resp *http.Response) error {
	var er struct {
		Error            string `json:"error"`
		Message          string `json:"msg"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	msg := er.Error
	if msg == "" {
		msg = er.Message
	}
	if msg == "" {
		msg = er.ErrorDescription
	}
	if msg == "" {
		msg = resp.Status
	}
	return errors.New(msg)
}

func filenameFromContentDisposition(header, fallback string) string {
	_, params, err := mime.ParseMediaType(header)
	if err == nil && params["filename"] != "" {
		return filepath.Base(params["filename"])
	}
	return fallback
}

func errNotInitialized() error {
	return fmt.Errorf("not initialized or joined; run context-drop init or context-drop join <token>")
}

var httpClient = &http.Client{Timeout: 60 * time.Second}
