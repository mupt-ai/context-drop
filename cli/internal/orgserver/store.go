// Package orgserver is the org side of the Context Drop server: orgs, the
// machines that joined them, and the relay that carries commands to the
// machine that owns an agent.
package orgserver

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"contextdrop.dev/context-drop/internal/pool"

	_ "modernc.org/sqlite"
)

// Org is a tenant. Its join token is stored only as a hash.
type Org struct {
	ID            string    `json:"id"`
	JoinTokenHash string    `json:"join_token_hash"`
	CreatedAt     time.Time `json:"created_at"`
}

// Machine is a computer that joined an org.
type Machine struct {
	Org      string    `json:"org"`
	Name     string    `json:"name"`
	CredHash string    `json:"cred_hash"`
	JoinedAt time.Time `json:"joined_at"`
}

// Store keeps orgs, machines, and extensions' state in a SQLite database. Each change writes only the
// rows it touches, and extensions save one org at a time, so the store grows with the number of orgs
// without slowing every write.
type Store struct {
	path    string
	db      *sql.DB
	mu      sync.Mutex // one writer at a time
	sealKey []byte     // seals extensions' tokens; nil means they cannot store any
	changed atomic.Bool
}

const schema = `
CREATE TABLE IF NOT EXISTS orgs (
	id TEXT PRIMARY KEY,
	join_token_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS orgs_token ON orgs(join_token_hash);
CREATE TABLE IF NOT EXISTS machines (
	org TEXT NOT NULL,
	name TEXT NOT NULL,
	cred_hash TEXT NOT NULL UNIQUE,
	joined_at TEXT NOT NULL,
	PRIMARY KEY (org, name)
);
CREATE TABLE IF NOT EXISTS extension_state (
	name TEXT NOT NULL,
	org TEXT NOT NULL,
	data TEXT NOT NULL,
	PRIMARY KEY (name, org)
);`

// OpenStore opens the database at path, given with or without .db. The JSON file older servers kept
// beside it (orgs.json for orgs.db) is copied in once, then renamed to .json.imported.
func OpenStore(path string) (*Store, error) {
	path = strings.TrimSuffix(path, ".json")
	if !strings.HasSuffix(path, ".db") {
		path += ".db"
	}
	legacy := strings.TrimSuffix(path, ".db") + ".json"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	s := &Store{path: path, db: db}
	if err := s.importJSON(legacy); err != nil {
		db.Close()
		return nil, fmt.Errorf("import %s: %w", legacy, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the database file.
func (s *Store) Path() string { return s.path }

// legacyState is the JSON file older servers kept.
type legacyState struct {
	Orgs       []Org                      `json:"orgs"`
	Machines   []Machine                  `json:"machines"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
	// Slack's state from before Slack became an extension.
	SlackInstalls json.RawMessage `json:"slack_installs,omitempty"`
	SlackMembers  json.RawMessage `json:"slack_members,omitempty"`
	SlackRoutes   json.RawMessage `json:"slack_routes,omitempty"`
}

// importJSON copies a JSON state file into an empty database, then renames the file so it is not
// imported again.
func (s *Store) importJSON(path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM orgs`).Scan(&n); err != nil || n > 0 {
		return err // the database already has data; leave the old file alone
	}
	var st legacyState
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	if st.SlackInstalls != nil || st.SlackMembers != nil || st.SlackRoutes != nil {
		if _, ok := st.Extensions["slack"]; !ok {
			b, err := json.Marshal(map[string]json.RawMessage{"installs": st.SlackInstalls, "members": st.SlackMembers, "routes": st.SlackRoutes})
			if err != nil {
				return err
			}
			if st.Extensions == nil {
				st.Extensions = map[string]json.RawMessage{}
			}
			st.Extensions["slack"] = b
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, o := range st.Orgs {
		if _, err := tx.Exec(`INSERT INTO orgs VALUES (?, ?, ?)`, o.ID, o.JoinTokenHash, o.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	for _, m := range st.Machines {
		if _, err := tx.Exec(`INSERT INTO machines VALUES (?, ?, ?, ?)`, m.Org, m.Name, m.CredHash, m.JoinedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	for name, blob := range st.Extensions {
		perOrg, err := splitByOrg(blob)
		if err != nil {
			return fmt.Errorf("extension %s: %w", name, err)
		}
		for org, data := range perOrg {
			if _, err := tx.Exec(`INSERT INTO extension_state VALUES (?, ?, ?)`, name, org, string(data)); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed.Store(true)
	return os.Rename(path, path+".imported")
}

// splitByOrg splits an extension's whole state, an object of lists of records that each carry an
// "org", into one object per org.
func splitByOrg(blob json.RawMessage) (map[string]json.RawMessage, error) {
	var lists map[string][]json.RawMessage
	if err := json.Unmarshal(blob, &lists); err != nil {
		return nil, err
	}
	byOrg := map[string]map[string][]json.RawMessage{}
	for key, list := range lists {
		for _, rec := range list {
			var r struct {
				Org string `json:"org"`
			}
			if err := json.Unmarshal(rec, &r); err != nil {
				return nil, err
			}
			if byOrg[r.Org] == nil {
				byOrg[r.Org] = map[string][]json.RawMessage{}
			}
			byOrg[r.Org][key] = append(byOrg[r.Org][key], rec)
		}
	}
	out := map[string]json.RawMessage{}
	for org, lists := range byOrg {
		b, err := json.Marshal(lists)
		if err != nil {
			return nil, err
		}
		out[org] = b
	}
	return out, nil
}

// LoadExtension calls each with every org's saved state for an extension.
func (s *Store) LoadExtension(name string, each func(org string, data json.RawMessage) error) error {
	rows, err := s.db.Query(`SELECT org, data FROM extension_state WHERE name = ? ORDER BY org`, name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var org, data string
		if err := rows.Scan(&org, &data); err != nil {
			return err
		}
		if err := each(org, json.RawMessage(data)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SaveExtension writes an extension's state for one org.
func (s *Store) SaveExtension(name, org string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.exec(`INSERT INTO extension_state VALUES (?, ?, ?) ON CONFLICT (name, org) DO UPDATE SET data = excluded.data`, name, org, string(raw))
}

func (s *Store) exec(q string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(q, args...); err != nil {
		return err
	}
	s.changed.Store(true)
	return nil
}

// HasOrg reports whether an org exists.
func (s *Store) HasOrg(id string) bool {
	var n int
	return s.db.QueryRow(`SELECT count(*) FROM orgs WHERE id = ?`, id).Scan(&n) == nil && n > 0
}

var orgID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Store errors, mapped to HTTP statuses by the server.
var (
	ErrExists   = errors.New("already exists")
	ErrNotFound = errors.New("not found")
	ErrBadName  = errors.New("names use lowercase letters, digits, and dashes, up to 63 characters")
)

// CreateOrg makes an org and returns its join token, which is shown only once.
func (s *Store) CreateOrg(id string) (token string, err error) {
	if !orgID.MatchString(id) || id == "orgs" { // "orgs" would collide with /v1/orgs/...
		return "", ErrBadName
	}
	if s.HasOrg(id) {
		return "", fmt.Errorf("org %s %w", id, ErrExists)
	}
	token = "cdj_" + secret()
	if err := s.exec(`INSERT INTO orgs VALUES (?, ?, ?)`, id, hash(token), now()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", fmt.Errorf("org %s %w", id, ErrExists)
		}
		return "", err
	}
	return token, nil
}

// RotateJoinToken replaces an org's join token. Machines already joined keep working.
func (s *Store) RotateJoinToken(id string) (string, error) {
	token := "cdj_" + secret()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE orgs SET join_token_hash = ? WHERE id = ?`, hash(token), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("org %s %w", id, ErrNotFound)
	}
	s.changed.Store(true)
	return token, nil
}

// Join adds a machine to the org whose join token this is and returns the
// machine's credential.
func (s *Store) Join(token, machine string) (org, credential string, err error) {
	if !pool.ValidMachineName(machine) {
		return "", "", ErrBadName
	}
	h := hash(token)
	if err := s.db.QueryRow(`SELECT id FROM orgs WHERE join_token_hash = ?`, h).Scan(&org); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("join token %w", ErrNotFound)
		}
		return "", "", err
	}
	credential = "cdm_" + secret()
	if err := s.exec(`INSERT INTO machines VALUES (?, ?, ?, ?)`, org, machine, hash(credential), now()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", "", fmt.Errorf("machine %s %w in %s; choose another name with --name, or run `context-drop leave` on it first", machine, ErrExists, org)
		}
		return "", "", err
	}
	return org, credential, nil
}

// Authenticate returns the machine a credential belongs to.
func (s *Store) Authenticate(credential string) (Machine, bool) {
	if !strings.HasPrefix(credential, "cdm_") {
		return Machine{}, false
	}
	var m Machine
	var at string
	err := s.db.QueryRow(`SELECT org, name, cred_hash, joined_at FROM machines WHERE cred_hash = ?`, hash(credential)).Scan(&m.Org, &m.Name, &m.CredHash, &at)
	if err != nil {
		return Machine{}, false
	}
	m.JoinedAt, _ = time.Parse(time.RFC3339Nano, at)
	return m, true
}

// Orgs returns every org's ID.
func (s *Store) Orgs() []string {
	out := []string{}
	rows, err := s.db.Query(`SELECT id FROM orgs ORDER BY created_at`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// Leave removes a machine and revokes its credential.
func (s *Store) Leave(org, machine string) error {
	return s.exec(`DELETE FROM machines WHERE org = ? AND name = ?`, org, machine)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func secret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Snapshot writes a consistent copy of the database to path, if anything changed since the last
// snapshot or force is set; it reports whether it wrote one.
func (s *Store) Snapshot(path string, force bool) (bool, error) {
	if !s.changed.Swap(false) && !force {
		return false, nil
	}
	_ = os.Remove(path)
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		s.changed.Store(true)
		return false, err
	}
	return true, nil
}
