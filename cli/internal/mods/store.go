package mods

import (
	"bytes"
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

// Mod is a deployed mod. Deploying again replaces it.
type Mod struct {
	Meta
	Author   string    `json:"author"`
	Deployed time.Time `json:"deployed"`
}

var (
	// ErrNotFound means the org has no such mod.
	ErrNotFound = errors.New("no such mod")
	nameRE      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// ValidName reports whether name can name a mod.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Store keeps mods on disk: <dir>/<org>/<name>/{mod.json, mod.ts, kv.json}.
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

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
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
	if err := readJSON(filepath.Join(s.modDir(org, name), "mod.json"), &m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// Get returns a mod and its source.
func (s *Store) Get(org, name string) (*Mod, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load(org, name)
	if err != nil {
		return nil, "", err
	}
	src, err := os.ReadFile(filepath.Join(s.modDir(org, name), "mod.ts"))
	return m, string(src), err
}

// List returns every mod in the org, by name.
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

// Put stores a mod, replacing any earlier one of the same name. Its key-value data is kept.
func (s *Store) Put(org string, meta Meta, source, author string) (Mod, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.modDir(org, meta.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Mod{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "mod.ts"), []byte(source), 0o600); err != nil {
		return Mod{}, err
	}
	m := Mod{Meta: meta, Author: author, Deployed: time.Now().UTC()}
	return m, writeJSON(filepath.Join(dir, "mod.json"), m)
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
	return value, true, writeJSON(path, kv)
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
