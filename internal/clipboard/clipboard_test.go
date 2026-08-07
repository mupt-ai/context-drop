package clipboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func setClipboardGOOS(t *testing.T, goos string) {
	t.Helper()
	old := clipboardGOOS
	clipboardGOOS = goos
	t.Cleanup(func() { clipboardGOOS = old })
}

func TestPipeSuccessAndFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	capture := filepath.Join(dir, "stdin")
	writeExecutable(t, dir, "capture", "#!/bin/sh\ncat > "+capture+"\n")
	if err := pipe(filepath.Join(dir, "capture"), nil, "hello"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("captured stdin = %q, want hello", data)
	}

	writeExecutable(t, dir, "fail", "#!/bin/sh\necho bad >&2\nexit 12\n")
	err = pipe(filepath.Join(dir, "fail"), nil, "hello")
	if err == nil || !strings.Contains(err.Error(), "fail failed") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("pipe(fail) error = %v, want command output", err)
	}
}

func TestCopyTextUsesPlatformClipboardTool(t *testing.T) {
	tests := []struct {
		name string
		goos string
		bin  string
	}{
		{name: "darwin", goos: "darwin", bin: "pbcopy"},
		{name: "linux", goos: "linux", bin: "wl-copy"},
		{name: "windows", goos: "windows", bin: "powershell.exe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setClipboardGOOS(t, tt.goos)
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			writeExecutable(t, dir, tt.bin, "#!/bin/sh\n/bin/cat > "+filepath.Join(dir, "copied")+"\n")

			if err := CopyText("copied text"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "copied"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "copied text" {
				t.Fatalf("copied text = %q, want copied text", data)
			}
		})
	}
}

func TestCopyTextLinuxFallbackToolsAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		bin  string
		args string
	}{
		{name: "xclip", bin: "xclip"},
		{name: "xsel", bin: "xsel"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setClipboardGOOS(t, "linux")
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			writeExecutable(t, dir, tt.bin, "#!/bin/sh\n/bin/cat > "+filepath.Join(dir, "copied")+"\n")
			if err := CopyText("fallback text"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "copied"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "fallback text" {
				t.Fatalf("copied text = %q, want fallback text", data)
			}
		})
	}

	setClipboardGOOS(t, "linux")
	t.Setenv("PATH", t.TempDir())

	err := CopyText("x")
	if err == nil || !strings.Contains(err.Error(), "no clipboard copy tool found") {
		t.Fatalf("CopyText() error = %v, want missing tool", err)
	}
}

func TestCopyTextUnsupportedOS(t *testing.T) {
	setClipboardGOOS(t, "plan9")

	err := CopyText("x")
	if err == nil || !strings.Contains(err.Error(), "clipboard copy unsupported on plan9") {
		t.Fatalf("CopyText() error = %v, want unsupported OS", err)
	}
}

func TestReadImagePNGUsesPlatformTool(t *testing.T) {
	tests := []struct {
		name string
		goos string
		bin  string
	}{
		{name: "darwin", goos: "darwin", bin: "pngpaste"},
		{name: "linux", goos: "linux", bin: "wl-paste"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setClipboardGOOS(t, tt.goos)
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			writeExecutable(t, dir, tt.bin, "#!/bin/sh\nprintf png-data\n")

			data, filename, err := ReadImagePNG()
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "png-data" || filename != "clipboard.png" {
				t.Fatalf("ReadImagePNG() = %q, %q, want png-data clipboard.png", data, filename)
			}
		})
	}
}

func TestReadImagePNGReportsMissingTool(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			setClipboardGOOS(t, goos)
			t.Setenv("PATH", t.TempDir())

			_, _, err := ReadImagePNG()
			if err == nil {
				t.Fatal("ReadImagePNG() error = nil, want missing tool error")
			}
		})
	}
}

func TestReadImagePNGFallsBackToXclip(t *testing.T) {
	setClipboardGOOS(t, "linux")
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	writeExecutable(t, dir, "wl-paste", "#!/bin/sh\nexit 1\n")
	writeExecutable(t, dir, "xclip", "#!/bin/sh\nprintf xclip-png\n")

	data, filename, err := ReadImagePNG()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "xclip-png" || filename != "clipboard.png" {
		t.Fatalf("ReadImagePNG() = %q, %q, want xclip-png clipboard.png", data, filename)
	}
}

func TestReadImagePNGReportsEmptyDarwinClipboard(t *testing.T) {
	setClipboardGOOS(t, "darwin")
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	writeExecutable(t, dir, "pngpaste", "#!/bin/sh\n")

	_, _, err := ReadImagePNG()
	if err == nil || !strings.Contains(err.Error(), "no PNG image found") {
		t.Fatalf("ReadImagePNG() error = %v, want empty clipboard", err)
	}
}

func TestReadImagePNGUnsupportedOS(t *testing.T) {
	setClipboardGOOS(t, "plan9")

	_, _, err := ReadImagePNG()
	if err == nil || !strings.Contains(err.Error(), "clipboard image read unsupported on plan9") {
		t.Fatalf("ReadImagePNG() error = %v, want unsupported OS", err)
	}
}
