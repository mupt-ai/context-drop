package agents

import (
	"os"
	"path/filepath"
	"strings"
)

// fileRef is a file placed in its git repository, so the same file in two
// worktrees of one repository compares equal by (repo, repoPath).
type fileRef struct {
	path     string // absolute, symlinks resolved
	root     string // worktree root, or "" outside a repository
	repo     string // common git directory shared by all worktrees
	repoPath string // path relative to root
}

func newFileRef(path string) fileRef {
	ref := fileRef{path: canonicalPath(path)}
	ref.root, ref.repo = repoOf(ref.path)
	if ref.root != "" {
		if rel, err := filepath.Rel(ref.root, ref.path); err == nil {
			ref.repoPath = filepath.ToSlash(rel)
		}
	}
	return ref
}

// key identifies the file across worktrees of one repository.
func (f fileRef) key() string {
	if f.repo != "" {
		return f.repo + "\x00" + f.repoPath
	}
	return f.path
}

// canonicalPath makes path absolute and resolves symlinks in its longest
// existing prefix, so a file that does not exist yet (or /tmp versus
// /private/tmp) still compares equal to its siblings.
func canonicalPath(path string) string {
	path, _ = filepath.Abs(path)
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if filepath.Dir(dir) == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// repoOf finds the git worktree containing path and its repository's common
// git directory. Both are "" outside a repository.
func repoOf(path string) (root, common string) {
	for dir := path; ; dir = filepath.Dir(dir) {
		git := filepath.Join(dir, ".git")
		if info, err := os.Stat(git); err == nil {
			if info.IsDir() {
				return dir, git
			}
			return dir, worktreeCommonDir(dir, git)
		}
		if filepath.Dir(dir) == dir {
			return "", ""
		}
	}
}

// worktreeCommonDir follows a linked worktree's .git file ("gitdir: …") to
// the main repository's git directory.
func worktreeCommonDir(dir, gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return gitFile
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	if gitdir == "" {
		return gitFile
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common := strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
		return canonicalPath(common)
	}
	return canonicalPath(gitdir)
}

// pathContains reports whether path is dir or inside it.
func pathContains(dir, path string) bool {
	if dir == "" || dir == "." {
		return true
	}
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}
