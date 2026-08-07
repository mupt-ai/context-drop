package cli

import "testing"

func TestErrNotInitialized(t *testing.T) {
	if got := errNotInitialized().Error(); got != "not initialized or joined; run context-drop init or context-drop join <token>" {
		t.Fatalf("errNotInitialized() = %q", got)
	}
}
