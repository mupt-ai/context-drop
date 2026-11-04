// Package update installs a published Context Drop release over the running binary.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Repo is the GitHub repository releases are published from.
const Repo = "mupt-ai/context-drop"

// BaseURL is where releases are served. Tests point it at a local server.
var BaseURL = "https://github.com/" + Repo

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9._+-]+)?$`)

// Normalize returns v with a leading "v", after checking it names a release.
func Normalize(v string) (string, error) {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("%q is not a release version such as v0.3.0", v)
	}
	return v, nil
}

// Latest returns the newest release tag, read from GitHub's latest-release redirect.
func Latest(ctx context.Context, client *http.Client) (string, error) {
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
		return "", fmt.Errorf("could not find the latest release (HTTP %d)", resp.StatusCode)
	}
	return Normalize(filepath.Base(loc))
}

// ArchiveName is the release archive for a version on a platform, as published.
func ArchiveName(version, goos, goarch string) (string, error) {
	osName := map[string]string{"darwin": "macOS", "linux": "linux"}[goos]
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[goarch]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("no release for %s/%s", goos, goarch)
	}
	return fmt.Sprintf("context-drop_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), osName, arch), nil
}

// Platform reports the archive name for this machine.
func Platform(version string) (string, error) {
	return ArchiveName(version, runtime.GOOS, runtime.GOARCH)
}

// Download fetches url into dest.
func Download(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// VerifyChecksum checks file against its line in a sha256sum-style checksums file.
func VerifyChecksum(file, checksums, name string) error {
	list, err := os.Open(checksums)
	if err != nil {
		return err
	}
	defer list.Close()
	var expected string
	scanner := bufio.NewScanner(list)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum file does not list %s", name)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected) {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

// ExtractBinary unpacks the context-drop binary from a release archive into dir.
func ExtractBinary(archive, dir string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", errors.New("release archive does not contain context-drop")
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "context-drop" {
			continue
		}
		out := filepath.Join(dir, "context-drop.extracted")
		o, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(o, tr); err != nil {
			o.Close()
			return "", err
		}
		return out, o.Close()
	}
}

// Replace swaps the file at target for newBin. The new file is written beside
// target and renamed over it, so an interrupted update never leaves a partial binary.
func Replace(newBin, target string) error {
	staged := filepath.Join(filepath.Dir(target), fmt.Sprintf(".context-drop.new.%d", os.Getpid()))
	if err := copyFile(newBin, staged); err != nil {
		return fmt.Errorf("cannot write next to %s: %w", target, err)
	}
	if err := os.Rename(staged, target); err != nil {
		os.Remove(staged)
		return fmt.Errorf("cannot replace %s: %w", target, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Install downloads version, verifies it, and replaces target with it.
func Install(ctx context.Context, client *http.Client, version, target string) error {
	archive, err := Platform(version)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "context-drop-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	base := BaseURL + "/releases/download/" + version
	checksums := fmt.Sprintf("context-drop_%s_checksums.txt", strings.TrimPrefix(version, "v"))
	archivePath := filepath.Join(dir, archive)
	checksumPath := filepath.Join(dir, checksums)
	if err := Download(ctx, client, base+"/"+archive, archivePath); err != nil {
		return err
	}
	if err := Download(ctx, client, base+"/"+checksums, checksumPath); err != nil {
		return err
	}
	if err := VerifyChecksum(archivePath, checksumPath, archive); err != nil {
		return err
	}
	bin, err := ExtractBinary(archivePath, dir)
	if err != nil {
		return err
	}
	return Replace(bin, target)
}
