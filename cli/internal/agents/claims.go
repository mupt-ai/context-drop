package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Claim is an advisory note that a session is working on a path or a task.
// It lasts while the session that made it is live.
type Claim struct {
	Session string `json:"session"`
	Agent   string `json:"agent"`
	// PID and Started identify the owner's process; the claim ends when it
	// exits (a reused PID has a different start time).
	PID     int       `json:"pid,omitempty"`
	Started time.Time `json:"started,omitempty"`
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
	case c.Path == "" || o.Path == "":
		// A task, or a name one agent saw as a path and another as a task.
		return sameTarget(c, o)
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

// ErrNoProcess means a session has no process to tie claims to.
var ErrNoProcess = errors.New("has no process Context Drop can follow, so its claims could not end with it")

// NewClaim builds a claim on target for owner. A target that exists, or
// that looks like a path (contains a slash or starts with "."), is a path,
// resolved against dir; anything else is a task name.
func NewClaim(owner Session, target, dir, note string, now time.Time) Claim {
	c := Claim{Session: owner.ID, Agent: owner.Agent, PID: owner.PID, Target: target, Note: note, ClaimedAt: now}
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
	env  *Env
}

// Claims returns the store at ~/.context-drop/claims.json.
func (e *Env) Claims() ClaimStore {
	return ClaimStore{Path: filepath.Join(e.ContextDropHome(), "claims.json"), env: e}
}

// Live returns the claims of the sessions in idx. It never changes the
// store, so a discovery pass that missed a session cannot drop its claims.
func (s ClaimStore) Live(idx Index) ([]Claim, error) {
	var stored []Claim
	if err := updateJSON(s.Path, &stored, func() (bool, error) { return false, nil }); err != nil {
		return nil, err
	}
	rebind(stored, idx, s.procs())
	live := map[string]bool{}
	for _, sess := range idx.Sessions {
		live[sess.ID] = true
	}
	return removeClaims(stored, func(c Claim) bool { return !live[c.Session] }), nil
}

// Add records claims for their owner, replacing the owner's earlier claim on
// the same target. Unless force is set, a claim that overlaps another
// session's claim is refused and nothing is recorded.
func (s ClaimStore) Add(idx Index, wanted []Claim, force bool) error {
	for _, w := range wanted {
		if w.PID <= 0 {
			return fmt.Errorf("%s %w (OpenCode sessions cannot claim)", w.Session, ErrNoProcess)
		}
	}
	if procs := s.procs(); procs != nil {
		for i := range wanted {
			wanted[i].Started = procs[wanted[i].PID].Start
		}
	}
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
// claims when targets is empty, and returns what it removed. A target matches
// a claim on the same path (in any worktree of the repository) or with the
// same name as given, so a file deleted since it was claimed still releases.
func (s ClaimStore) Release(idx Index, session string, targets []Claim) ([]Claim, error) {
	var released []Claim
	err := s.update(idx, func(claims []Claim) ([]Claim, error) {
		return removeClaims(claims, func(c Claim) bool {
			if c.Session != session {
				return false
			}
			match := len(targets) == 0
			for _, t := range targets {
				match = match || sameClaim(c, t) || sameTarget(c, t)
			}
			if match {
				released = append(released, c)
			}
			return match
		}), nil
	})
	return released, err
}

// update changes the stored claims under the lock. It first drops claims
// whose owner has ended: its process is gone, or, for a session without a
// process (OpenCode), it is not in idx. When the process table cannot be
// read, nothing is dropped.
func (s ClaimStore) update(idx Index, fn func([]Claim) ([]Claim, error)) error {
	var stored []Claim
	return updateJSON(s.Path, &stored, func() (bool, error) {
		procs := s.procs()
		rebind(stored, idx, procs)
		claims := removeClaims(stored, func(c Claim) bool { return claimEnded(c, idx, procs) })
		updated, err := fn(claims)
		if err != nil {
			return false, err
		}
		sort.SliceStable(updated, func(i, j int) bool { return updated[i].ClaimedAt.Before(updated[j].ClaimedAt) })
		changed := len(updated) != len(stored) || !sameClaims(updated, stored)
		stored = updated
		return changed, nil
	})
}

// claimEnded reports whether a claim's owner has ended. Its process exiting
// (or its PID being reused) ends it. A process that serves several sessions
// (a Codex app-server) outlives them, so such a claim also ends when its
// session is no longer discovered, unless that agent's discovery failed this
// time. When the process table cannot be read, nothing has ended.
func claimEnded(c Claim, idx Index, procs map[int]Process) bool {
	if procs == nil || c.PID <= 0 {
		return false
	}
	if !c.running(procs) {
		return true
	}
	if !sharedServer(procs[c.PID].Command) {
		return false
	}
	for _, s := range idx.Sessions {
		if s.ID == c.Session {
			return false
		}
	}
	for _, w := range idx.Warnings {
		if strings.HasPrefix(w, c.Agent+":") {
			return false
		}
	}
	return true
}

// procs reads the process table, or returns nil when it cannot be read, in
// which case nothing is judged dead.
func (s ClaimStore) procs() map[int]Process {
	if s.env == nil {
		return nil
	}
	procs, err := s.env.Procs(context.Background())
	if err != nil {
		return nil
	}
	return procs
}

// running reports whether the claim's process still runs: same PID and, when
// recorded, the same start time.
func (c Claim) running(procs map[int]Process) bool {
	p, ok := procs[c.PID]
	return ok && (c.Started.IsZero() || p.Start.Equal(c.Started))
}

// rebind moves claims of a Pi session that changed ID to its new ID: without
// the extension it takes its file's ID after its first message but keeps its
// process. Only the same process (PID and start time) counts. No other agent
// changes ID, so a claim of another kind is never moved (a Codex app-server
// may be left serving one session after the claimant's thread ended).
func rebind(claims []Claim, idx Index, procs map[int]Process) {
	live := map[string]bool{}
	byPID := map[int][]Session{}
	for _, s := range idx.Sessions {
		live[s.ID] = true
		if !s.Subagent && s.PID > 0 {
			byPID[s.PID] = append(byPID[s.PID], s)
		}
	}
	for i, c := range claims {
		if found := byPID[c.PID]; c.Agent == Pi && !live[c.Session] && c.PID > 0 && len(found) == 1 && found[0].Agent == Pi && procs != nil && c.running(procs) {
			claims[i].Session = found[0].ID
		}
	}
}

// sameClaim reports whether two claims are on the same path (compared within
// the repository, so across its worktrees) or the same task.
func sameClaim(a, b Claim) bool {
	switch {
	case a.Path == "" && b.Path == "":
		return sameTarget(a, b)
	case a.Path == "" || b.Path == "":
		return false
	case a.Repo != "" && a.Repo == b.Repo:
		return a.RepoPath == b.RepoPath
	}
	return a.Path == b.Path
}

// sameTarget compares what was claimed, as given.
func sameTarget(a, b Claim) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(a.Target), "/"), strings.Trim(strings.TrimSpace(b.Target), "/"))
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

func sameClaims(a, b []Claim) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
