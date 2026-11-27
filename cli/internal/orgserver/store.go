// Package orgserver is the org side of the Context Drop server: orgs, the
// machines that joined them, and the relay that carries commands to the
// machine that owns an agent.
package orgserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"contextdrop.dev/context-drop/internal/db"
	"contextdrop.dev/context-drop/internal/pool"
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

// Store keeps orgs, machines, extensions' state, and which server each machine is connected to, in a
// database: SQLite for one server, or Postgres shared by several. Each change writes only the rows it
// touches.
type Store struct {
	db      *db.DB
	path    string     // the SQLite file; empty on Postgres
	mu      sync.Mutex // guards sealKey
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
);
CREATE TABLE IF NOT EXISTS extension_keys (
	name TEXT NOT NULL,
	key TEXT NOT NULL,
	org TEXT NOT NULL,
	PRIMARY KEY (name, key)
);
CREATE TABLE IF NOT EXISTS extension_once (
	name TEXT NOT NULL,
	id TEXT NOT NULL,
	at BIGINT NOT NULL,
	PRIMARY KEY (name, id)
);
CREATE TABLE IF NOT EXISTS servers (
	id TEXT PRIMARY KEY,
	seen BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS presence (
	org TEXT NOT NULL,
	machine TEXT NOT NULL,
	server TEXT NOT NULL,
	conn TEXT NOT NULL,
	agents TEXT NOT NULL,
	seen BIGINT NOT NULL,
	PRIMARY KEY (org, machine)
);
CREATE INDEX IF NOT EXISTS presence_server ON presence(server);
CREATE TABLE IF NOT EXISTS relay (
	id TEXT PRIMARY KEY,
	from_server TEXT NOT NULL,
	to_server TEXT NOT NULL,
	org TEXT NOT NULL,
	machine TEXT NOT NULL,
	method TEXT NOT NULL,
	input TEXT NOT NULL,
	timeout_ms BIGINT NOT NULL,
	output TEXT,
	error TEXT,
	done BOOLEAN NOT NULL DEFAULT FALSE,
	created BIGINT NOT NULL
)`

// OpenStore opens the store at addr: a postgres:// URL or DSN, or a SQLite file given with or without
// .db. For SQLite, the JSON file older servers kept beside it (orgs.json for orgs.db) is copied in
// once, then renamed to .json.imported.
func OpenStore(addr string) (*Store, error) {
	if db.IsPostgres(addr) {
		d, err := db.Open(addr)
		if err != nil {
			return nil, err
		}
		if err := d.Migrate(schema); err != nil {
			d.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
		return &Store{db: d}, nil
	}
	path := strings.TrimSuffix(addr, ".json")
	if !strings.HasSuffix(path, ".db") {
		path += ".db"
	}
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := d.Migrate(schema); err != nil {
		d.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{db: d, path: path}
	legacy := strings.TrimSuffix(path, ".db") + ".json"
	if _, err := os.Stat(legacy); err == nil {
		if err := s.ImportJSON(legacy); err != nil {
			d.Close()
			return nil, fmt.Errorf("import %s: %w", legacy, err)
		}
		if err := os.Rename(legacy, legacy+".imported"); err != nil {
			d.Close()
			return nil, err
		}
	}
	return s, nil
}

// DB is the store's database, which mods keep their state in too.
func (s *Store) DB() *db.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the SQLite file, or empty on Postgres.
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

// ImportJSON copies the JSON state file older servers kept into the database. Orgs, machines, and
// extension state the database already has are kept, so importing twice changes nothing.
func (s *Store) ImportJSON(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
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
	err = s.db.InTx(context.Background(), func(tx *db.Tx) error {
		for _, o := range st.Orgs {
			if _, err := tx.Exec(`INSERT INTO orgs VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, o.ID, o.JoinTokenHash, o.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		for _, m := range st.Machines {
			if _, err := tx.Exec(`INSERT INTO machines VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, m.Org, m.Name, m.CredHash, m.JoinedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		for name, blob := range st.Extensions {
			perOrg, err := splitByOrg(blob)
			if err != nil {
				return fmt.Errorf("extension %s: %w", name, err)
			}
			for org, data := range perOrg {
				if _, err := tx.Exec(`INSERT INTO extension_state VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, name, org, string(data)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.changed.Store(true)
	return nil
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

// EachExtension calls each with every org's saved state for an extension.
func (s *Store) EachExtension(name string, each func(org string, data json.RawMessage) error) error {
	rows, err := s.db.Query(`SELECT org, data FROM extension_state WHERE name = ? ORDER BY org`, name)
	if err != nil {
		return err
	}
	type row struct{ org, data string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.org, &r.data); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range all {
		if err := each(r.org, json.RawMessage(r.data)); err != nil {
			return err
		}
	}
	return nil
}

// GetExtension reads an extension's state for one org into v; it reports false when there is none.
func (s *Store) GetExtension(name, org string, v any) (bool, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM extension_state WHERE name = ? AND org = ?`, name, org).Scan(&data)
	if db.NoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(data), v)
}

// UpdateExtension reads an org's state into v, runs fn, and writes v back, holding the row so no
// other server changes it in between. fn must not use the store. An error from fn writes nothing.
func (s *Store) UpdateExtension(ctx context.Context, name, org string, v any, fn func() error) error {
	err := s.db.InTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(`INSERT INTO extension_state VALUES (?, ?, '{}') ON CONFLICT (name, org) DO NOTHING`, name, org); err != nil {
			return err
		}
		var data string
		if err := tx.QueryRow(`SELECT data FROM extension_state WHERE name = ? AND org = ?`+tx.ForUpdate(), name, org).Scan(&data); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(data), v); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE extension_state SET data = ? WHERE name = ? AND org = ?`, string(raw), name, org)
		return err
	})
	if err == nil {
		s.changed.Store(true)
	}
	return err
}

// SaveExtension writes an extension's state for one org.
func (s *Store) SaveExtension(name, org string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.exec(`INSERT INTO extension_state VALUES (?, ?, ?) ON CONFLICT (name, org) DO UPDATE SET data = excluded.data`, name, org, string(raw))
}

// ErrTaken means another org holds an extension key, such as a Slack workspace.
var ErrTaken = errors.New("held by another org")

// ClaimKey gives an extension key, such as a Slack workspace ID, to an org; it fails with ErrTaken when
// another org holds it. Claiming a key the org already holds is fine.
func (s *Store) ClaimKey(name, key, org string) error {
	if err := s.exec(`INSERT INTO extension_keys VALUES (?, ?, ?) ON CONFLICT (name, key) DO NOTHING`, name, key, org); err != nil {
		return err
	}
	owner, _, err := s.KeyOwner(name, key)
	if err != nil {
		return err
	}
	if owner != org {
		return ErrTaken
	}
	return nil
}

// ReleaseKey lets go of a key the org holds.
func (s *Store) ReleaseKey(name, key, org string) error {
	return s.exec(`DELETE FROM extension_keys WHERE name = ? AND key = ? AND org = ?`, name, key, org)
}

// KeyOwner returns the org holding a key.
func (s *Store) KeyOwner(name, key string) (string, bool, error) {
	var org string
	err := s.db.QueryRow(`SELECT org FROM extension_keys WHERE name = ? AND key = ?`, name, key).Scan(&org)
	if db.NoRows(err) {
		return "", false, nil
	}
	return org, err == nil, err
}

// onceTTL is how long Once remembers an ID.
const onceTTL = time.Hour

// Once reports whether this is the first time any server has seen an ID, such as a webhook's event
// ID, within the last hour. On a database error it reports true, so nothing is dropped.
func (s *Store) Once(name, id string) bool {
	if id == "" {
		return true
	}
	res, err := s.db.Exec(`INSERT INTO extension_once VALUES (?, ?, ?) ON CONFLICT (name, id) DO NOTHING`, name, id, time.Now().UnixMilli())
	if err != nil {
		return true
	}
	n, _ := res.RowsAffected()
	return n == 1
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
		if db.Duplicate(err) {
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
		if errors.Is(err, db.ErrNoRows) {
			return "", "", fmt.Errorf("join token %w", ErrNotFound)
		}
		return "", "", err
	}
	credential = "cdm_" + secret()
	if err := s.exec(`INSERT INTO machines VALUES (?, ?, ?, ?)`, org, machine, hash(credential), now()); err != nil {
		if db.Duplicate(err) {
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
	if s.db.PG {
		return false, nil
	}
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

// Unsnapshotted marks the database as changed again, so the next Snapshot writes a copy; for when
// the last copy never reached its backup.
func (s *Store) Unsnapshotted() { s.changed.Store(true) }
