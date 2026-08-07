package pairing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalStorePersistsStateWithoutRawTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	store := NewLocal(path)
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	principal, sessionToken, firstMachine, err := store.CreateChain(context.Background(), "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	inviteToken, _, err := store.CreateInvite(context.Background(), principal, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), inviteToken) || strings.Contains(string(data), sessionToken) {
		t.Fatalf("pairing store persisted raw token material: %s", data)
	}

	reloaded := NewLocal(path)
	joined, _, joinedMachine, err := reloaded.ConsumeInvite(context.Background(), inviteToken, "joined", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if joined.ChainID != principal.ChainID {
		t.Fatalf("joined chain = %q, want %q", joined.ChainID, principal.ChainID)
	}
	if joinedMachine.ID == firstMachine.ID {
		t.Fatalf("joined machine reused first machine ID %q", joinedMachine.ID)
	}
	_, _, _, err = reloaded.ConsumeInvite(context.Background(), inviteToken, "reuse", now.Add(2*time.Second))
	if err != ErrInvalidInvite {
		t.Fatalf("reused invite error = %v, want invalid invite", err)
	}
}
