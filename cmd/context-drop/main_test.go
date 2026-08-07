package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReturnsCommandError(t *testing.T) {
	t.Setenv("CONTEXT_DROP_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	var stdout, stderr bytes.Buffer
	err := run([]string{"token", "create"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("run(token create) error = %v, want not initialized", err)
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"version"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "context-drop") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMainErrorExits(t *testing.T) {
	t.Setenv("CONTEXT_DROP_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	oldExit := exit
	defer func() { exit = oldExit }()
	code := -1
	exit = func(c int) { code = c }

	oldArgs := setArgs([]string{"context-drop", "token", "create"})
	defer setArgs(oldArgs)
	main()
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func setArgs(args []string) []string {
	old := os.Args
	os.Args = args
	return old
}
