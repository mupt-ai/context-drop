package localhome

import (
	"os"
	"path/filepath"
)

// Root returns the single private root for Context Drop local runtime and daemon state.
// Root returns the single private root for Context Drop local runtime and daemon state.
// It defaults to ~/.context-drop (mirroring the layout users expect from a dot-config
// home); CONTEXT_DROP_HOME overrides it for tests and relocation.
func Root() (string, error) {
	if value := os.Getenv("CONTEXT_DROP_HOME"); value != "" {
		return filepath.Clean(value), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".context-drop"), nil
}

func Ensure() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, os.Chmod(root, 0o700)
}
