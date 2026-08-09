package cli

import (
	"errors"
	"testing"

	"contextdrop.dev/context-drop/internal/runtimeclient"
)

func TestRuntimeNodePathPrefersPersistedPath(t *testing.T) {
	got, err := runtimeNodePath(runtimeclient.RuntimeConfig{NodePath: "/private/node"}, func(string) (string, error) {
		t.Fatal("resolver should not be called for a configured Node path")
		return "", nil
	})
	if err != nil || got != "/private/node" {
		t.Fatalf("runtimeNodePath() = %q, %v", got, err)
	}
}

func TestRuntimeNodePathFallsBackToResolver(t *testing.T) {
	got, err := runtimeNodePath(runtimeclient.RuntimeConfig{}, func(name string) (string, error) {
		if name != "node" {
			t.Fatalf("resolve name = %q", name)
		}
		return "/fallback/node", nil
	})
	if err != nil || got != "/fallback/node" {
		t.Fatalf("runtimeNodePath() = %q, %v", got, err)
	}
	_, err = runtimeNodePath(runtimeclient.RuntimeConfig{}, func(string) (string, error) { return "", errors.New("not found") })
	if err == nil || err.Error() != "resolve Node runtime: not found" {
		t.Fatalf("runtimeNodePath() error = %v", err)
	}
}
