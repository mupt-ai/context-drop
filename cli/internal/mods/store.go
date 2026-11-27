package mods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/db"
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

// Store keeps mods, and each mod's key-value data, in the server's database, so every server runs the
// same mods.
type Store struct {
	db *db.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS mods (
	org TEXT NOT NULL,
	name TEXT NOT NULL,
	meta TEXT NOT NULL,
	source TEXT NOT NULL,
	author TEXT NOT NULL,
	deployed TEXT NOT NULL,
	PRIMARY KEY (org, name)
);
CREATE TABLE IF NOT EXISTS mod_kv (
	org TEXT NOT NULL,
	mod TEXT NOT NULL,
	key TEXT NOT NULL,
	value TEXT NOT NULL,
	PRIMARY KEY (org, mod, key)
)`

// OpenStore keeps mods in d.
func OpenStore(d *db.DB) (*Store, error) {
	if err := d.Migrate(schema); err != nil {
		return nil, err
	}
	return &Store{db: d}, nil
}

func scanMod(meta, author, deployed string) (*Mod, error) {
	var m Mod
	if err := json.Unmarshal([]byte(meta), &m.Meta); err != nil {
		return nil, err
	}
	m.Author = author
	m.Deployed, _ = time.Parse(time.RFC3339Nano, deployed)
	return &m, nil
}

// Get returns a mod and its source.
func (s *Store) Get(org, name string) (*Mod, string, error) {
	var meta, src, author, deployed string
	err := s.db.QueryRow(`SELECT meta, source, author, deployed FROM mods WHERE org = ? AND name = ?`, org, name).Scan(&meta, &src, &author, &deployed)
	if db.NoRows(err) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	m, err := scanMod(meta, author, deployed)
	return m, src, err
}

// List returns every mod in the org, by name.
func (s *Store) List(org string) ([]*Mod, error) {
	rows, err := s.db.Query(`SELECT meta, author, deployed FROM mods WHERE org = ? ORDER BY name`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Mod
	for rows.Next() {
		var meta, author, deployed string
		if err := rows.Scan(&meta, &author, &deployed); err != nil {
			return nil, err
		}
		if m, err := scanMod(meta, author, deployed); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

// Put stores a mod, replacing any earlier one of the same name. Its key-value data is kept.
func (s *Store) Put(org string, meta Meta, source, author string) (Mod, error) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return Mod{}, err
	}
	m := Mod{Meta: meta, Author: author, Deployed: time.Now().UTC()}
	_, err = s.db.Exec(`INSERT INTO mods VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (org, name) DO UPDATE
		SET meta = excluded.meta, source = excluded.source, author = excluded.author, deployed = excluded.deployed`,
		org, meta.Name, string(raw), source, author, m.Deployed.Format(time.RFC3339Nano))
	return m, err
}

// kvLimit bounds one mod's key-value data.
const kvLimit = 10 << 20

// KV runs one key-value operation on a mod's own store. For cas, expected nil means "absent".
func (s *Store) KV(org, name, op, key string, value, expected json.RawMessage) (json.RawMessage, bool, error) {
	if key == "" || len(key) > 512 {
		return nil, false, errors.New("key must be 1 to 512 characters")
	}
	var result json.RawMessage
	var ok bool
	err := s.db.InTx(context.Background(), func(tx *db.Tx) error {
		var cur string
		err := tx.QueryRow(`SELECT value FROM mod_kv WHERE org = ? AND mod = ? AND key = ?`+tx.ForUpdate(), org, name, key).Scan(&cur)
		found := err == nil
		if err != nil && !db.NoRows(err) {
			return err
		}
		switch op {
		case "get":
			if found {
				result = json.RawMessage(cur)
			}
			return nil
		case "set":
		case "cas":
			same := (!found && isNull(expected)) || (found && !isNull(expected) && jsonEqual(json.RawMessage(cur), expected))
			if !same {
				if found {
					result = json.RawMessage(cur)
				}
				return nil
			}
		default:
			return fmt.Errorf("unknown kv op %q", op)
		}
		if isNull(value) {
			_, err = tx.Exec(`DELETE FROM mod_kv WHERE org = ? AND mod = ? AND key = ?`, org, name, key)
		} else {
			var size int64
			if err := tx.QueryRow(`SELECT COALESCE(SUM(LENGTH(key) + LENGTH(value)), 0) FROM mod_kv WHERE org = ? AND mod = ? AND key <> ?`, org, name, key).Scan(&size); err != nil {
				return err
			}
			if size+int64(len(key)+len(value)) > kvLimit {
				return errors.New("a mod's key-value store is limited to 10 MB")
			}
			_, err = tx.Exec(`INSERT INTO mod_kv VALUES (?, ?, ?, ?) ON CONFLICT (org, mod, key) DO UPDATE SET value = excluded.value`, org, name, key, string(value))
		}
		result, ok = value, true
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return result, ok, nil
}

// ImportDir copies mods older servers kept on disk, <dir>/<org>/<name>/{mod.json, mod.ts, kv.json},
// into the database, skipping any mod it already has.
func (s *Store) ImportDir(dir string) (int, error) {
	orgs, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range orgs {
		mods, _ := os.ReadDir(filepath.Join(dir, o.Name()))
		for _, md := range mods {
			base := filepath.Join(dir, o.Name(), md.Name())
			var m Mod
			raw, err := os.ReadFile(filepath.Join(base, "mod.json"))
			if err != nil || json.Unmarshal(raw, &m) != nil {
				continue
			}
			src, err := os.ReadFile(filepath.Join(base, "mod.ts"))
			if err != nil {
				continue
			}
			if _, _, err := s.Get(o.Name(), m.Name); err == nil {
				continue
			}
			meta, _ := json.Marshal(m.Meta)
			if _, err := s.db.Exec(`INSERT INTO mods VALUES (?, ?, ?, ?, ?, ?)`, o.Name(), m.Name, string(meta), string(src), m.Author, m.Deployed.UTC().Format(time.RFC3339Nano)); err != nil {
				return n, err
			}
			kv := map[string]json.RawMessage{}
			if raw, err := os.ReadFile(filepath.Join(base, "kv.json")); err == nil {
				_ = json.Unmarshal(raw, &kv)
			}
			for k, v := range kv {
				if _, err := s.db.Exec(`INSERT INTO mod_kv VALUES (?, ?, ?, ?) ON CONFLICT (org, mod, key) DO NOTHING`, o.Name(), m.Name, k, string(v)); err != nil {
					return n, err
				}
			}
			n++
		}
	}
	return n, nil
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
