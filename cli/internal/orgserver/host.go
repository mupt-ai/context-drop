package orgserver

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"

	"contextdrop.dev/context-drop/internal/extensions"
	"contextdrop.dev/context-drop/internal/pool"
)

// SetSealKey sets the key extensions' tokens are sealed with at rest. Without one, no extension can
// store a token.
func (s *Store) SetSealKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		s.sealKey = nil
		return
	}
	sum := sha256.Sum256([]byte(key))
	s.sealKey = sum[:]
}

func (s *Store) gcm() (cipher.AEAD, error) {
	s.mu.Lock()
	key := s.sealKey
	s.mu.Unlock()
	if key == nil {
		return nil, errors.New("no token key is configured (CONTEXT_DROP_TOKEN_KEY)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts a secret for storage.
func (s *Store) Seal(plain string) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Unseal decrypts a secret Seal made.
func (s *Store) Unseal(sealed string) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("sealed token is too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot unseal a stored token; was the token key changed?")
	}
	return string(plain), nil
}

// host is what the server gives its extensions.
type host struct{ s *Server }

func (h host) Authed(fn func(http.ResponseWriter, *http.Request, extensions.Caller)) http.HandlerFunc {
	return h.s.authed(func(w http.ResponseWriter, r *http.Request, c caller) { fn(w, r, c.ext()) })
}

func (h host) Deliver(ctx context.Context, org, agent, text, from string) *pool.Error {
	machine, session, ok := pool.SplitID(agent)
	if !ok {
		return pool.Errorf("bad_request", "%q is not an agent ID", agent)
	}
	_, perr := h.s.call(ctx, org, machine, "send", pool.LocalSend{ID: session, Text: text, From: from})
	return perr
}

func (h host) HasOrg(org string) bool               { return h.s.store.HasOrg(org) }
func (h host) Load(name string, v any) error        { return h.s.store.LoadExtension(name, v) }
func (h host) Save(name string, v any) error        { return h.s.store.SaveExtension(name, v) }
func (h host) Seal(plain string) (string, error)    { return h.s.store.Seal(plain) }
func (h host) Unseal(sealed string) (string, error) { return h.s.store.Unseal(sealed) }
func (h host) PublicURL() string                    { return h.s.PublicURL }
func (h host) Log() *slog.Logger                    { return h.s.log }

// ext is the caller as extensions see it.
func (c caller) ext() extensions.Caller {
	if c.mod != nil {
		return extensions.NewCaller(c.org, "", c.mod.Mod, c.mod.Can)
	}
	return extensions.NewCaller(c.org, c.machine, "", nil)
}

// extension returns the extension that owns a send target.
func (s *Server) extension(target string) extensions.Extension {
	for _, e := range s.Extensions {
		if e.Owns(target) {
			return e
		}
	}
	return nil
}
