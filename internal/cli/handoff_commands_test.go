package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"contextdrop.dev/context-drop/internal/handoff"
)

func TestCreateStagingDirRejectsArbitraryParent(t *testing.T) {
	t.Parallel()
	if _, err := createStagingDir(filepath.Join(t.TempDir(), "parent"), "hnd_x"); err == nil {
		t.Fatal("expected arbitrary parent rejection")
	}
}

func TestCreateStagingDirPrivate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := createStagingDir("", "hnd_x")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
func TestVerifyArtifact(t *testing.T) {
	t.Parallel()
	data := []byte("hello")
	sum := sha256.Sum256(data)
	a := handoff.Artifact{Filename: "x", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if err := verifyArtifact(a, data); err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifact(a, []byte("bad")); err == nil {
		t.Fatal("expected mismatch")
	}
}
