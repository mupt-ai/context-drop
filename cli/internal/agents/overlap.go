package agents

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Overlap kinds.
const (
	// OverlapWorktree: several live sessions work in one git worktree.
	OverlapWorktree = "worktree"
	// OverlapFile: several live sessions edited the same file, in one worktree
	// or in different worktrees of one repository.
	OverlapFile = "file"
	// OverlapClaim: a session edited a file another session has claimed.
	OverlapClaim = "claim"
)

// Overlap is a place where live sessions may be stepping on each other.
type Overlap struct {
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	Sessions []string `json:"sessions"`
	Detail   string   `json:"detail"`
}

// WorktreeOverlaps groups top-level sessions that share a git worktree.
func WorktreeOverlaps(sessions []Session) []Overlap {
	byRoot := map[string][]string{}
	for _, s := range sessions {
		if s.Subagent || s.Cwd == "" {
			continue
		}
		if root, _ := repoOf(canonicalPath(s.Cwd)); root != "" {
			byRoot[root] = append(byRoot[root], s.ID)
		}
	}
	var out []Overlap
	for root, ids := range byRoot {
		if len(ids) > 1 {
			sort.Strings(ids)
			out = append(out, Overlap{Kind: OverlapWorktree, Path: root, Sessions: ids,
				Detail: fmt.Sprintf("%d sessions share this worktree", len(ids))})
		}
	}
	sortOverlaps(out)
	return out
}

// FindOverlaps reports shared worktrees, files edited by more than one
// session since the given time, and edits to files another session claimed.
// Only top-level sessions count; a subagent's edits are not attributed.
func FindOverlaps(sessions []Session, claims []Claim, since time.Time) []Overlap {
	out := WorktreeOverlaps(sessions)

	type editor struct {
		id   string
		root string
	}
	byFile := map[string][]editor{}
	files := map[string]fileRef{}
	edited := map[string][]fileRef{} // session ID → files it edited
	for _, s := range sessions {
		if s.Subagent {
			continue
		}
		seen := map[string]bool{}
		for _, e := range EditedFiles(s, since) {
			f := newFileRef(e.Path)
			if seen[f.key()] {
				continue
			}
			seen[f.key()] = true
			files[f.key()] = f
			byFile[f.key()] = append(byFile[f.key()], editor{s.ID, f.root})
			edited[s.ID] = append(edited[s.ID], f)
		}
	}
	for key, editors := range byFile {
		if len(editors) < 2 {
			continue
		}
		ids, roots := []string{}, map[string]bool{}
		for _, e := range editors {
			ids = append(ids, e.id)
			roots[e.root] = true
		}
		sort.Strings(ids)
		detail := "edited by each of them in the same worktree"
		if len(roots) > 1 {
			detail = fmt.Sprintf("edited in %d worktrees of one repository; expect merge conflicts", len(roots))
		}
		f := files[key]
		out = append(out, Overlap{Kind: OverlapFile, Path: displayPath(f), Sessions: ids, Detail: detail})
	}
	for _, c := range claims {
		var others []string
		for id, refs := range edited {
			if id == c.Session {
				continue
			}
			for _, f := range refs {
				if c.covers(f) {
					others = append(others, id)
					break
				}
			}
		}
		if len(others) > 0 {
			sort.Strings(others)
			out = append(out, Overlap{Kind: OverlapClaim, Path: c.Path, Sessions: append([]string{c.Session}, others...),
				Detail: fmt.Sprintf("claimed by %s; edited by %s", c.Session, strings.Join(others, ", "))})
		}
	}
	sortOverlaps(out)
	return out
}

func displayPath(f fileRef) string {
	if f.repoPath != "" {
		return f.repoPath
	}
	return f.path
}

func sortOverlaps(out []Overlap) {
	rank := map[string]int{OverlapClaim: 0, OverlapFile: 1, OverlapWorktree: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Path < out[j].Path
	})
}
