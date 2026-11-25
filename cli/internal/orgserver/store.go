// Package orgserver is the org side of the Context Drop server: orgs, the
// machines that joined them, and the relay that carries commands to the
// machine that owns an agent.
package orgserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	"time"

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

type state struct {
	Orgs          []Org          `json:"orgs"`
	Machines      []Machine      `json:"machines"`
	SlackInstalls []SlackInstall `json:"slack_installs,omitempty"`
	SlackMembers  []SlackMember  `json:"slack_members,omitempty"`
	SlackRoutes   []SlackRoute   `json:"slack_routes,omitempty"`
}

// Store keeps orgs and machines in one JSON file. It is enough for a single
// server; a multi-instance relay moves this to Postgres.
type Store struct {
	path     string
	mu       sync.Mutex
	st       state
	slackKey []byte // seals Slack bot tokens; nil turns Slack off
}

// OpenStore loads the store at path, creating it on first write.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.st.Orgs {
		if o.ID == id {
			return "", fmt.Errorf("org %s %w", id, ErrExists)
		}
	}
	token = "cdj_" + secret()
	s.st.Orgs = append(s.st.Orgs, Org{ID: id, JoinTokenHash: hash(token), CreatedAt: time.Now().UTC()})
	return token, s.save()
}

// RotateJoinToken replaces an org's join token. Machines already joined keep working.
func (s *Store) RotateJoinToken(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Orgs {
		if s.st.Orgs[i].ID == id {
			token := "cdj_" + secret()
			s.st.Orgs[i].JoinTokenHash = hash(token)
			return token, s.save()
		}
	}
	return "", fmt.Errorf("org %s %w", id, ErrNotFound)
}

// Join adds a machine to the org whose join token this is and returns the
// machine's credential.
func (s *Store) Join(token, machine string) (org, credential string, err error) {
	if !pool.ValidMachineName(machine) {
		return "", "", ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hash(token)
	for _, o := range s.st.Orgs {
		if subtle.ConstantTimeCompare([]byte(o.JoinTokenHash), []byte(h)) != 1 {
			continue
		}
		for _, m := range s.st.Machines {
			if m.Org == o.ID && m.Name == machine {
				return "", "", fmt.Errorf("machine %s %w in %s; choose another name with --name, or run `context-drop leave` on it first", machine, ErrExists, o.ID)
			}
		}
		credential = "cdm_" + secret()
		s.st.Machines = append(s.st.Machines, Machine{Org: o.ID, Name: machine, CredHash: hash(credential), JoinedAt: time.Now().UTC()})
		return o.ID, credential, s.save()
	}
	return "", "", fmt.Errorf("join token %w", ErrNotFound)
}

// Authenticate returns the machine a credential belongs to.
func (s *Store) Authenticate(credential string) (Machine, bool) {
	if !strings.HasPrefix(credential, "cdm_") {
		return Machine{}, false
	}
	h := hash(credential)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.st.Machines {
		if subtle.ConstantTimeCompare([]byte(m.CredHash), []byte(h)) == 1 {
			return m, true
		}
	}
	return Machine{}, false
}

// Leave removes a machine and revokes its credential.
// Orgs returns every org's ID.
func (s *Store) Orgs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.st.Orgs))
	for _, o := range s.st.Orgs {
		out = append(out, o.ID)
	}
	return out
}

func (s *Store) Leave(org, machine string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.st.Machines[:0]
	for _, m := range s.st.Machines {
		if !(m.Org == org && m.Name == machine) {
			kept = append(kept, m)
		}
	}
	s.st.Machines = kept
	return s.save()
}

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
