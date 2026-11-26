// Package extensions connects an org to the places its people already talk, such as Slack and Linear.
//
// An extension owns a kind of send target ("#deploys", "linear:ENG-123"), posts to it for agents, and
// pushes people's replies back to the agents that are waiting on them. The org server hosts every
// extension through Host; each extension lives in its own package under this one.
package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"

	"contextdrop.dev/context-drop/internal/pool"
)

// Extension is one place people talk.
type Extension interface {
	// Name is the extension's name, its URL prefix, and the capability a mod needs to use it.
	Name() string
	// Owns reports whether a send target belongs to this extension.
	Owns(target string) bool
	// Register adds the extension's routes and loads its saved state.
	Register(mux *http.ServeMux, h Host) error
	// Send posts a message for people. replyTo is the sending agent's org ID, or empty when no agent
	// sent it (a mod, or a person at a terminal); replies to an agent's message are routed back to it.
	Send(ctx context.Context, c Caller, target, text, from, replyTo string) (pool.SendResult, *pool.Error)
}

// Caller is who made a request: a machine in an org, or a running mod.
type Caller struct {
	Org     string
	Machine string // empty for a mod
	Mod     string // the mod's name, empty for a machine
	can     map[string]bool
}

// NewCaller makes a caller. A mod may use only the capabilities in can; a machine may use them all.
func NewCaller(org, machine, mod string, can map[string]bool) Caller {
	return Caller{Org: org, Machine: machine, Mod: mod, can: can}
}

// May reports whether the caller may use a capability.
func (c Caller) May(capability string) bool { return c.Mod == "" || c.can[capability] }

// Host is what the org server gives an extension.
type Host interface {
	// Authed admits requests carrying a credential for the org in the path, a machine's or a mod's.
	Authed(func(http.ResponseWriter, *http.Request, Caller)) http.HandlerFunc
	// Deliver pushes text into one agent's session, as its next prompt.
	Deliver(ctx context.Context, org, agent, text, from string) *pool.Error
	// HasOrg reports whether an org exists.
	HasOrg(org string) bool
	// Load calls each with every org's saved state for the extension.
	Load(name string, each func(org string, data json.RawMessage) error) error
	// Save writes the extension's state for one org. Saving per org keeps each write small.
	Save(name, org string, v any) error
	// Seal encrypts a secret, such as a token, for storage; Unseal reverses it.
	Seal(plain string) (string, error)
	Unseal(sealed string) (string, error)
	// PublicURL is the server's public origin, for OAuth redirects and webhooks.
	PublicURL() string
	Log() *slog.Logger
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the server's error shape.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, map[string]*pool.Error{"error": {Code: code, Message: msg}})
}

// Fail writes a pool error with its matching status.
func Fail(w http.ResponseWriter, e *pool.Error) { WriteError(w, StatusFor(e), e.Code, e.Message) }

// Decode reads a JSON request body into v; it writes the error and returns false when it can't.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// StatusFor maps an error code to an HTTP status.
func StatusFor(e *pool.Error) int {
	switch e.Code {
	case "not_found":
		return http.StatusNotFound
	case "forbidden":
		return http.StatusForbidden
	case "offline":
		return http.StatusServiceUnavailable
	case "timeout":
		return http.StatusGatewayTimeout
	case "unsupported", "bad_request":
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// MachinesOnly refuses mods, for endpoints only a machine may use.
func MachinesOnly(w http.ResponseWriter, c Caller) bool {
	if c.Mod != "" {
		WriteError(w, http.StatusForbidden, "forbidden", "mods cannot use this endpoint")
		return false
	}
	return true
}

// Allowed refuses a mod whose can list lacks capability.
func Allowed(w http.ResponseWriter, c Caller, capability string) bool {
	if !c.May(capability) {
		WriteError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("mod %s needs %q in its can list", c.Mod, capability))
		return false
	}
	return true
}

// Page writes a small HTML page for a person in a browser. body is HTML; escape what goes in it.
func Page(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\"><title>Context Drop</title>"+
		"<body style=\"font-family:system-ui;max-width:36rem;margin:4rem auto;padding:0 1rem\"><h1>%s</h1>%s", html.EscapeString(title), body)
}
