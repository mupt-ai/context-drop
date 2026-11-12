package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Claim is an advisory note that a session is working on a path or a task.
// It lasts while the session that made it is live.
type Claim struct {
	Session string `json:"session"`
	Agent   string `json:"agent"`
	// Target is what was claimed, as given.
	Target string `json:"target"`
	// Path is set when Target names a file or directory: its absolute path.
	Path string `json:"path,omitempty"`
	// Repo and RepoPath place Path in its git repository (the common git
	// directory and the path within its worktree), so claims in different
	// worktrees of one repository are compared.
	Repo      string    `json:"repo,omitempty"`
	RepoPath  string    `json:"repo_path,omitempty"`
	Note      string    `json:"note,omitempty"`
	ClaimedAt time.Time `json:"claimed_at"`
}

// Overlaps reports whether two claims cover the same thing: one path
// contains the other, or two tasks have the same name.
func (c Claim) Overlaps(o Claim) bool {
	switch {
	case c.Path == "" && o.Path == "":
		return strings.EqualFold(strings.TrimSpace(c.Target), strings.TrimSpace(o.Target))
	case c.Path == "" || o.Path == "":
		return false
	case c.Repo != "" && c.Repo == o.Repo:
		return pathContains(c.RepoPath, o.RepoPath) || pathContains(o.RepoPath, c.RepoPath)
	}
	return pathContains(c.Path, o.Path) || pathContains(o.Path, c.Path)
}

// covers reports whether the claim covers a file.
func (c Claim) covers(f fileRef) bool {
	if c.Path == "" {
		return false
	}
	if c.Repo != "" && c.Repo == f.repo {
		return pathContains(c.RepoPath, f.repoPath)
	}
	return pathContains(c.Path, f.path)
}

// ClaimConflict is another session's claim that overlaps a new one.
type ClaimConflict struct {
	Wanted Claim
	Held   Claim
}

func (c ClaimConflict) Error() string {
	return fmt.Sprintf("%s is claimed by %s (%s)", c.Wanted.Target, c.Held.Session, c.Held.Target)
}

// NewClaim builds a claim on target for owner. A target that exists, or
// that looks like a path (contains a slash or starts with "."), is a path,
// resolved against dir; anything else is a task name.
func NewClaim(owner Session, target, dir, note string, now time.Time) Claim {
	c := Claim{Session: owner.ID, Agent: owner.Agent, Target: target, Note: note, ClaimedAt: now}
	path := target
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if _, err := os.Stat(path); err == nil || strings.ContainsRune(target, '/') || strings.HasPrefix(target, ".") {
		ref := newFileRef(path)
		c.Path, c.Repo, c.RepoPath = ref.path, ref.repo, ref.repoPath
	}
	return c
}

// ClaimStore keeps claims in a JSON file guarded by an exclusive lock, so
// agents can claim concurrently.
type ClaimStore struct {
	Path string
}

// Claims returns the store at ~/.context-drop/claims.json.
func (e *Env) Claims() ClaimStore {
	return ClaimStore{Path: filepath.Join(e.ContextDropHome(), "claims.json")}
}

// Live returns the claims whose sessions are in idx.
func (s ClaimStore) Live(idx Index) ([]Claim, error) {
	var out []Claim
	err := s.update(idx, func(claims []Claim) ([]Claim, error) {
		out = claims
		return claims, nil
	})
	return out, err
}

// Add records claims for their owner, replacing the owner's earlier claim on
// the same target. Unless force is set, a claim that overlaps another live
// session's claim is refused and nothing is recorded.
func (s ClaimStore) Add(idx Index, wanted []Claim, force bool) error {
	return s.update(idx, func(claims []Claim) ([]Claim, error) {
		var conflicts []error
		for _, w := range wanted {
			for _, h := range claims {
				if h.Session != w.Session && h.Overlaps(w) {
					conflicts = append(conflicts, ClaimConflict{w, h})
				}
			}
		}
		if len(conflicts) > 0 && !force {
			return nil, errors.Join(conflicts...)
		}
		for _, w := range wanted {
			claims = removeClaims(claims, func(c Claim) bool { return c.Session == w.Session && sameClaim(c, w) })
			claims = append(claims, w)
		}
		return claims, nil
	})
}

// Release removes the session's claims on the given targets, or all of its
// claims when targets is empty, and returns what it removed.
func (s ClaimStore) Release(idx Index, session string, targets []Claim) ([]Claim, error) {
	var released []Claim
	err := s.update(idx, func(claims []Claim) ([]Claim, error) {
		return removeClaims(claims, func(c Claim) bool {
			if c.Session != session {
				return false
			}
			match := len(targets) == 0
			for _, t := range targets {
				match = match || sameClaim(c, t)
			}
			if match {
				released = append(released, c)
			}
			return match
		}), nil
	})
	return released, err
}

func sameClaim(a, b Claim) bool {
	if a.Path != "" || b.Path != "" {
		return a.Path == b.Path
	}
	return strings.EqualFold(strings.TrimSpace(a.Target), strings.TrimSpace(b.Target))
}

func removeClaims(claims []Claim, drop func(Claim) bool) []Claim {
	kept := claims[:0:0]
	for _, c := range claims {
		if !drop(c) {
			kept = append(kept, c)
		}
	}
	return kept
}

// update loads the claims under the lock, drops those of sessions that are no
// longer live, applies fn, and saves the result.
func (s ClaimStore) update(idx Index, fn func([]Claim) ([]Claim, error)) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	var stored []Claim
	data, err := os.ReadFile(s.Path)
	if err == nil {
		if err := json.Unmarshal(data, &stored); err != nil {
			return fmt.Errorf("read %s: %w", s.Path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	live := map[string]bool{}
	for _, sess := range idx.Sessions {
		live[sess.ID] = true
	}
	claims := removeClaims(stored, func(c Claim) bool { return !live[c.Session] })
	updated, err := fn(claims)
	if err != nil {
		return err
	}
	sort.SliceStable(updated, func(i, j int) bool { return updated[i].ClaimedAt.Before(updated[j].ClaimedAt) })
	if len(updated) == len(stored) && sameClaims(updated, stored) {
		return nil // unchanged
	}
	data, err = json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

func sameClaims(a, b []Claim) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
