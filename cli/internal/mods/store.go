package mods

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version is one deploy of a mod.
type Version struct {
	Meta
	Version int       `json:"version"`
	Author  string    `json:"author"`
	Created time.Time `json:"created"`
	SHA     string    `json:"sha"`
}

// Mod is a mod's history. Owner is set for a preview (`name@machine`), which only that machine sees.
type Mod struct {
	Name     string    `json:"name"`
	Owner    string    `json:"owner,omitempty"`
	Active   int       `json:"active"`
	Versions []Version `json:"versions"`
}

// Current is the active version.
func (m *Mod) Current() *Version {
	for i := range m.Versions {
		if m.Versions[i].Version == m.Active {
			return &m.Versions[i]
		}
	}
	return nil
}

var (
	// ErrNotFound means no such mod is visible to the caller.
	ErrNotFound = errors.New("no such mod")
	nameRE      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// ValidName reports whether name can name a mod.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Store keeps mods on disk: <dir>/<org>/<name>/{meta.json, v<N>.ts, kv.json, secrets.json}.
type Store struct {
	dir string
	mu  sync.Mutex
}

// OpenStore uses dir, creating it.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) modDir(org, name string) string { return filepath.Join(s.dir, org, name) }

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Store) load(org, name string) (*Mod, error) {
	var m Mod
	if err := readJSON(filepath.Join(s.modDir(org, name), "meta.json"), &m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// Get returns a mod and its active source.
func (s *Store) Get(org, name string) (*Mod, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load(org, name)
	if err != nil {
		return nil, "", err
	}
	src, err := os.ReadFile(filepath.Join(s.modDir(org, name), fmt.Sprintf("v%d.ts", m.Active)))
	return m, string(src), err
}

// List returns every mod in the org.
func (s *Store) List(org string) ([]*Mod, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, org))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Mod
	for _, e := range entries {
		if m, err := s.load(org, e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Put stores a new version and makes it active.
func (s *Store) Put(org, name, owner string, meta Meta, source, author string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load(org, name)
	if errors.Is(err, ErrNotFound) {
		m, err = &Mod{Name: name, Owner: owner}, nil
	}
	if err != nil {
		return Version{}, err
	}
	sum := sha256.Sum256([]byte(source))
	v := Version{Meta: meta, Version: 1, Author: author, Created: time.Now().UTC(), SHA: hex.EncodeToString(sum[:])}
	if n := len(m.Versions); n > 0 {
		v.Version = m.Versions[n-1].Version + 1
	}
	dir := s.modDir(org, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Version{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("v%d.ts", v.Version)), []byte(source), 0o600); err != nil {
		return Version{}, err
	}
	m.Versions = append(m.Versions, v)
	m.Active = v.Version
	return v, writeJSON(filepath.Join(dir, "meta.json"), m, 0o600)
}

// Rollback makes the version before the active one active.
func (s *Store) Rollback(org, name string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load(org, name)
	if err != nil {
		return 0, err
	}
	prev := 0
	for _, v := range m.Versions {
		if v.Version < m.Active && v.Version > prev {
			prev = v.Version
		}
	}
	if prev == 0 {
		return 0, fmt.Errorf("%s has no version before %d", name, m.Active)
	}
	m.Active = prev
	return prev, writeJSON(filepath.Join(s.modDir(org, name), "meta.json"), m, 0o600)
}

// SetSecret stores a secret for one mod.
func (s *Store) SetSecret(org, name, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.load(org, name); err != nil {
		return err
	}
	path := filepath.Join(s.modDir(org, name), "secrets.json")
	secrets := map[string]string{}
	_ = readJSON(path, &secrets)
	secrets[key] = value
	return writeJSON(path, secrets, 0o600)
}

// Secrets returns a mod's secrets.
func (s *Store) Secrets(org, name string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	secrets := map[string]string{}
	_ = readJSON(filepath.Join(s.modDir(org, name), "secrets.json"), &secrets)
	return secrets
}

// KV runs one key-value operation on a mod's own store. For cas, expected nil means "absent".
func (s *Store) KV(org, name, op, key string, value, expected json.RawMessage) (json.RawMessage, bool, error) {
	if key == "" || len(key) > 512 {
		return nil, false, errors.New("key must be 1 to 512 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.modDir(org, name), "kv.json")
	kv := map[string]json.RawMessage{}
	if err := readJSON(path, &kv); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	switch op {
	case "get":
		return kv[key], false, nil
	case "set":
		kv[key] = value
	case "cas":
		cur, ok := kv[key]
		same := (!ok && isNull(expected)) || (ok && !isNull(expected) && jsonEqual(cur, expected))
		if !same {
			return cur, false, nil
		}
		kv[key] = value
	default:
		return nil, false, fmt.Errorf("unknown kv op %q", op)
	}
	if isNull(value) {
		delete(kv, key)
	}
	size := 0
	for k, v := range kv {
		size += len(k) + len(v)
	}
	if size > 10<<20 {
		return nil, false, errors.New("a mod's key-value store is limited to 10 MB")
	}
	return value, true, writeJSON(path, kv, 0o600)
}

func isNull(v json.RawMessage) bool {
	t := strings.TrimSpace(string(v))
	return t == "" || t == "null"
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}
