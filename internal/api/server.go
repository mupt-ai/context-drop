package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/drop"
	"contextdrop.dev/context-drop/internal/storage"
)

type Server struct {
	baseURL     string
	store       storage.Store
	uploadToken string
	defaultTTL  time.Duration
	maxTTL      time.Duration
	maxBytes    int64
	log         *slog.Logger
}

type Options struct {
	BaseURL     string
	Store       storage.Store
	UploadToken string
	DefaultTTL  time.Duration
	MaxTTL      time.Duration
	MaxBytes    int64
	Logger      *slog.Logger
}

func NewServer(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		baseURL:     strings.TrimRight(opts.BaseURL, "/"),
		store:       opts.Store,
		uploadToken: opts.UploadToken,
		defaultTTL:  opts.DefaultTTL,
		maxTTL:      opts.MaxTTL,
		maxBytes:    opts.MaxBytes,
		log:         logger,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /v1/drops", s.handleCreateDrop)
	mux.HandleFunc("GET /d/{id}", s.handleGetDrop)
	return secureHeaders(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleCreateDrop(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeUpload(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="context-drop-upload"`)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filename := drop.SafeFilename(r.Header.Get("X-Filename"))
	if filename == "drop" {
		filename = "upload"
	}

	ttl, err := s.parseTTL(r.Header.Get("X-TTL"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.maxBytes+1)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds max size of %d bytes", s.maxBytes))
		return
	}
	if int64(len(data)) > s.maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds max size of %d bytes", s.maxBytes))
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "upload is empty")
		return
	}

	id, err := drop.NewID()
	if err != nil {
		s.log.Error("generate drop id", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create drop")
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = contentTypeFromName(filename)
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = parsed
	}

	now := time.Now().UTC()
	digest := sha256.Sum256(data)
	meta := drop.Metadata{
		ID:          id,
		ObjectKey:   "drops/" + id + "/" + drop.BlobName,
		Filename:    filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(digest[:]),
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
	}

	if err := s.store.Put(r.Context(), meta, bytes.NewReader(data)); err != nil {
		s.log.Error("store drop", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to store drop")
		return
	}

	resp := createDropResponse{
		ID:          id,
		URL:         s.dropURL(id),
		ExpiresAt:   meta.ExpiresAt,
		ContentType: meta.ContentType,
		Size:        meta.Size,
	}
	s.log.Info("created drop", "id", id, "size", meta.Size, "expires_at", meta.ExpiresAt)
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) authorizeUpload(r *http.Request) bool {
	const prefix = "Bearer "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) || s.uploadToken == "" {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.uploadToken)) == 1
}

func (s *Server) handleGetDrop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !drop.ValidID(id) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	meta, err := s.store.GetMeta(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		s.log.Error("get metadata", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "failed to load drop")
		return
	}
	if time.Now().UTC().After(meta.ExpiresAt) {
		writeError(w, http.StatusGone, "drop expired")
		return
	}
	s.serveBlob(w, r, meta)
}

func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, meta drop.Metadata) {
	blob, err := s.store.GetBlob(r.Context(), meta)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		s.log.Error("get blob", "err", err, "id", meta.ID)
		writeError(w, http.StatusInternalServerError, "failed to load drop")
		return
	}
	defer blob.Close()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("Content-Disposition", contentDisposition(meta.ContentType, meta.Filename))
	if meta.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	_, _ = io.Copy(w, blob)
}

func (s *Server) parseTTL(raw string) (time.Duration, error) {
	if raw == "" {
		return s.defaultTTL, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid X-TTL")
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("ttl must be positive")
	}
	if ttl > s.maxTTL {
		return 0, fmt.Errorf("ttl exceeds max of %s", s.maxTTL)
	}
	return ttl, nil
}

func (s *Server) dropURL(id string) string {
	return s.baseURL + "/d/" + id
}

func contentTypeFromName(filename string) string {
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
}

func contentDisposition(contentType, filename string) string {
	disposition := "attachment"
	if isSafeInline(contentType) {
		disposition = "inline"
	}
	return mime.FormatMediaType(disposition, map[string]string{"filename": drop.SafeFilename(filename)})
}

func isSafeInline(contentType string) bool {
	switch strings.ToLower(contentType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain":
		return true
	default:
		return false
	}
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type createDropResponse struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expires_at"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
