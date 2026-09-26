package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type UploadRequest struct {
	Endpoint    string
	UploadToken string
	Filename    string
	ContentType string
	TTL         time.Duration
	Data        []byte
}

type UploadResponse struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expires_at"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
}

func Upload(ctx context.Context, req UploadRequest) (UploadResponse, error) {
	if req.Endpoint == "" {
		return UploadResponse{}, fmt.Errorf("endpoint is required")
	}
	if req.UploadToken == "" {
		return UploadResponse{}, fmt.Errorf("upload token is required; set CONTEXT_DROP_UPLOAD_TOKEN or upload_token in config")
	}
	if len(req.Data) == 0 {
		return UploadResponse{}, fmt.Errorf("upload data is empty")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(req.Endpoint, "/")+"/v1/drops", bytes.NewReader(req.Data))
	if err != nil {
		return UploadResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+req.UploadToken)
	httpReq.Header.Set("X-Filename", req.Filename)
	if req.ContentType != "" {
		httpReq.Header.Set("Content-Type", req.ContentType)
	}
	if req.TTL > 0 {
		httpReq.Header.Set("X-TTL", req.TTL.String())
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return UploadResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return UploadResponse{}, fmt.Errorf("upload failed: %w", readError(resp))
	}
	var out UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UploadResponse{}, fmt.Errorf("upload failed: %w", err)
	}
	return out, nil
}

func readError(resp *http.Response) error {
	var body struct {
		Error            string `json:"error"`
		Message          string `json:"msg"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	message := body.Error
	if message == "" {
		message = body.Message
	}
	if message == "" {
		message = body.ErrorDescription
	}
	if message == "" {
		message = resp.Status
	}
	return errors.New(message)
}

var httpClient = &http.Client{Timeout: 60 * time.Second}
