package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/handoff"
	"contextdrop.dev/context-drop/internal/pairing"
	"contextdrop.dev/context-drop/internal/storage"
)

type createHandoffRequest struct {
	To              string             `json:"to"`
	Summary         string             `json:"summary"`
	RequestedAction string             `json:"requested_action,omitempty"`
	Repository      string             `json:"repository,omitempty"`
	BaseCommit      string             `json:"base_commit,omitempty"`
	SourceRunID     string             `json:"source_run_id,omitempty"`
	Artifacts       []handoff.Artifact `json:"artifacts,omitempty"`
	TTL             string             `json:"ttl,omitempty"`
}

type handoffStateRequest struct {
	State handoff.State `json:"state"`
}
type handoffListResponse struct {
	Handoffs []handoff.Handoff `json:"handoffs"`
}

func (s *Server) handleCreateHandoff(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	if s.handoffStore == nil {
		writeError(w, http.StatusNotImplemented, "handoffs are unavailable")
		return
	}
	var req createHandoffRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ttl := s.defaultTTL
	var err error
	if req.TTL != "" {
		ttl, err = time.ParseDuration(req.TTL)
	}
	if err != nil || ttl <= 0 || ttl > s.maxTTL {
		writeError(w, http.StatusBadRequest, "invalid handoff ttl")
		return
	}
	now := time.Now().UTC()
	for i := range req.Artifacts {
		meta, err := s.store.GetMeta(r.Context(), req.Artifacts[i].DropID)
		if err != nil || meta.ChainID != principal.ChainID || !now.Before(meta.ExpiresAt) {
			writeError(w, http.StatusBadRequest, "artifact is missing, expired, or outside this chain")
			return
		}
		if now.Add(ttl).After(meta.ExpiresAt) {
			writeError(w, http.StatusBadRequest, "handoff ttl exceeds artifact lifetime")
			return
		}
		req.Artifacts[i].Filename = meta.Filename
		req.Artifacts[i].Size = meta.Size
		req.Artifacts[i].ContentType = meta.ContentType
		req.Artifacts[i].SHA256 = meta.SHA256
	}
	h, err := s.handoffStore.CreateHandoff(r.Context(), principal, handoff.Create{
		TargetMachineID: req.To, Summary: req.Summary, RequestedAction: req.RequestedAction,
		Repository: req.Repository, BaseCommit: req.BaseCommit, SourceRunID: req.SourceRunID,
		Artifacts: req.Artifacts, TTL: ttl,
	}, now)
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleListHandoffs(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	if s.handoffStore == nil {
		writeError(w, http.StatusNotImplemented, "handoffs are unavailable")
		return
	}
	hs, err := s.handoffStore.ListHandoffs(r.Context(), principal, time.Now().UTC())
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, handoffListResponse{Handoffs: hs})
}

func (s *Server) handleGetHandoff(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	if s.handoffStore == nil {
		writeError(w, http.StatusNotImplemented, "handoffs are unavailable")
		return
	}
	h, err := s.handoffStore.GetHandoff(r.Context(), principal, r.PathValue("id"), time.Now().UTC())
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleHandoffState(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	if s.handoffStore == nil {
		writeError(w, http.StatusNotImplemented, "handoffs are unavailable")
		return
	}
	var req handoffStateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.State != handoff.StateInspected && req.State != handoff.StateAccepting && req.State != handoff.StateAccepted && req.State != handoff.StateRejected {
		writeError(w, http.StatusBadRequest, "invalid recipient state")
		return
	}
	h, err := s.handoffStore.SetHandoffState(r.Context(), principal, r.PathValue("id"), req.State, time.Now().UTC())
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func decodeJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func writeHandoffError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pairing.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, pairing.ErrMachineNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, pairing.ErrAmbiguousMachine), errors.Is(err, handoff.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case strings.Contains(err.Error(), "expired"):
		writeError(w, http.StatusGone, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
