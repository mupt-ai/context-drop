package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeAndArchiveName(t *testing.T) {
	if v, err := Normalize("0.3.0"); err != nil || v != "v0.3.0" {
		t.Fatalf("Normalize = %q, %v", v, err)
	}
	for _, bad := range []string{"latest", "v1", "../x"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) accepted", bad)
		}
	}
	if got, _ := ArchiveName("v0.3.0", "darwin", "arm64"); got != "context-drop_0.3.0_macOS_arm64.tar.gz" {
		t.Errorf("darwin arm64 = %s", got)
	}
	if got, _ := ArchiveName("v0.3.0", "linux", "amd64"); got != "context-drop_0.3.0_linux_x86_64.tar.gz" {
		t.Errorf("linux amd64 = %s", got)
	}
	if _, err := ArchiveName("v0.3.0", "windows", "amd64"); err == nil {
		t.Error("windows accepted")
	}
}

// release builds a fake GitHub release: a latest-release redirect, an archive
// holding a binary with the given contents, and a checksums file.
func release(t *testing.T, version, binary string, corrupt bool) *httptest.Server {
	t.Helper()
	archiveName, err := Platform(version)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "context-drop", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	tw.Write([]byte(binary))
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	recorded := hex.EncodeToString(sum[:])
	if corrupt {
		recorded = strings.Repeat("0", 64)
	}
	checksums := fmt.Sprintf("%s  %s\n", recorded, archiveName)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+version, http.StatusFound)
		case "/releases/download/" + version + "/" + archiveName:
			w.Write(buf.Bytes())
		case "/releases/download/" + version + "/context-drop_" + strings.TrimPrefix(version, "v") + "_checksums.txt":
			fmt.Fprint(w, checksums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	return srv
}

func TestLatestReadsRedirect(t *testing.T) {
	release(t, "v0.4.0", "x", false)
	got, err := Latest(context.Background(), http.DefaultClient)
	if err != nil || got != "v0.4.0" {
		t.Fatalf("Latest = %q, %v", got, err)
	}
}

func TestInstallReplacesBinaryAfterChecksum(t *testing.T) {
	release(t, "v0.4.0", "NEW-BINARY", false)
	target := filepath.Join(t.TempDir(), "context-drop")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), http.DefaultClient, "v0.4.0", target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "NEW-BINARY" {
		t.Fatalf("binary = %q", data)
	}
}

func TestInstallKeepsOldBinaryOnChecksumMismatch(t *testing.T) {
	release(t, "v0.4.0", "NEW-BINARY", true)
	target := filepath.Join(t.TempDir(), "context-drop")
	os.WriteFile(target, []byte("OLD"), 0o755)
	if err := Install(context.Background(), http.DefaultClient, "v0.4.0", target); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "OLD" {
		t.Fatalf("binary changed to %q", data)
	}
}
