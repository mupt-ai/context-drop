package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/pairing"
)

func (s *Server) handleCreateChain(w http.ResponseWriter, r *http.Request) {
	var req createChainRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	principal, sessionToken, machine, err := s.pairingStore.CreateChain(r.Context(), req.MachineName, time.Now().UTC())
	if err != nil {
		writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createChainResponse{
		ChainID:      principal.ChainID,
		MachineID:    principal.MachineID,
		MachineName:  machine.Name,
		SessionToken: sessionToken,
	})
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req createInviteRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	now := time.Now().UTC()
	principal, ok := s.requirePairingSession(w, r, now)
	if !ok {
		return
	}
	machine, err := s.pairingMachine(r.Context(), principal)
	if err != nil {
		s.log.Error("load pairing machine", "err", err, "chain_id", principal.ChainID)
		writeError(w, http.StatusInternalServerError, "failed to load machine")
		return
	}

	ttl, err := inviteTTL(req.TTL, s.joinTokenTTL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, invite, err := s.pairingStore.CreateInvite(r.Context(), principal, ttl, now)
	if err != nil {
		writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createInviteResponse{
		Token:       token,
		ExpiresAt:   invite.ExpiresAt,
		ChainID:     principal.ChainID,
		MachineID:   principal.MachineID,
		MachineName: machine.Name,
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	principal, sessionToken, machine, err := s.pairingStore.ConsumeInvite(
		r.Context(),
		req.Token,
		req.MachineName,
		time.Now().UTC(),
	)
	if err != nil {
		writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, joinResponse{
		ChainID:      principal.ChainID,
		MachineID:    principal.MachineID,
		MachineName:  machine.Name,
		SessionToken: sessionToken,
	})
}

func (s *Server) handleListMachines(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	machines, err := s.pairingStore.ListMachines(r.Context(), principal)
	if err != nil {
		writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listMachinesResponse{Machines: machines})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	var req sendMessageRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	msg, err := s.pairingStore.SendMessage(
		r.Context(),
		principal,
		req.To,
		req.Body,
		time.Now().UTC(),
	)
	if err != nil {
		writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, messageResponse{Message: msg})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePairingSession(w, r, time.Now().UTC())
	if !ok {
		return
	}
	messages, err := s.pairingStore.ListMessages(r.Context(), principal)
	if err != nil {
		writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listMessagesResponse{Messages: messages})
}

func (s *Server) requirePairingSession(w http.ResponseWriter, r *http.Request, now time.Time) (pairing.Principal, bool) {
	principal, ok, err := s.authenticatePairingSession(r, now)
	if err != nil {
		s.log.Error("authenticate pairing session", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to authenticate pairing session")
		return pairing.Principal{}, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return pairing.Principal{}, false
	}
	return principal, true
}

func (s *Server) authenticatePairingSession(r *http.Request, now time.Time) (pairing.Principal, bool, error) {
	header := r.Header.Get("Authorization")
	if s.pairingStore == nil || !strings.HasPrefix(header, "Bearer ") {
		return pairing.Principal{}, false, nil
	}
	return s.pairingStore.AuthenticateSession(
		r.Context(),
		strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")),
		now,
	)
}

func (s *Server) pairingMachine(ctx context.Context, principal pairing.Principal) (pairing.Machine, error) {
	machines, err := s.pairingStore.ListMachines(ctx, principal)
	if err != nil {
		return pairing.Machine{}, err
	}
	for _, machine := range machines {
		if machine.ID == principal.MachineID {
			return machine, nil
		}
	}
	return pairing.Machine{}, pairing.ErrMachineNotFound
}

type createChainRequest struct {
	MachineName string `json:"machine_name"`
}

type createChainResponse struct {
	ChainID      string `json:"chain_id"`
	MachineID    string `json:"machine_id"`
	MachineName  string `json:"machine_name"`
	SessionToken string `json:"session_token"`
}

type createInviteRequest struct {
	TTL string `json:"ttl"`
}

type createInviteResponse struct {
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ChainID      string    `json:"chain_id"`
	MachineID    string    `json:"machine_id"`
	MachineName  string    `json:"machine_name"`
	SessionToken string    `json:"session_token,omitempty"`
}

type joinRequest struct {
	Token       string `json:"token"`
	MachineName string `json:"machine_name"`
}

type joinResponse struct {
	ChainID      string `json:"chain_id"`
	MachineID    string `json:"machine_id"`
	MachineName  string `json:"machine_name"`
	SessionToken string `json:"session_token"`
}

type listMachinesResponse struct {
	Machines []pairing.Machine `json:"machines"`
}

type sendMessageRequest struct {
	To   string `json:"to"`
	Body string `json:"body"`
}

type messageResponse struct {
	Message pairing.Message `json:"message"`
}

type listMessagesResponse struct {
	Messages []pairing.Message `json:"messages"`
}

func inviteTTL(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.New("invalid ttl")
	}
	return ttl, nil
}

func writePairingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pairing.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, pairing.ErrInvalidInvite):
		writeError(w, http.StatusUnauthorized, "invalid or expired join token")
	case errors.Is(err, pairing.ErrMachineNotFound):
		writeError(w, http.StatusNotFound, "machine not found")
	case errors.Is(err, pairing.ErrAmbiguousMachine):
		writeError(w, http.StatusBadRequest, "machine name is ambiguous")
	case errors.Is(err, pairing.ErrMessageEmpty):
		writeError(w, http.StatusBadRequest, "message is empty")
	case errors.Is(err, pairing.ErrConflict):
		writeError(w, http.StatusConflict, "pairing state conflict")
	case strings.Contains(err.Error(), "ttl") || strings.Contains(err.Error(), "message exceeds"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "pairing operation failed")
	}
}
