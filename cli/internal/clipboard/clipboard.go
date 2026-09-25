package clipboard

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
)

var clipboardGOOS = runtime.GOOS

func CopyText(text string) error {
	switch clipboardGOOS {
	case "darwin":
		return pipe("pbcopy", nil, text)
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return pipe("wl-copy", nil, text)
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			return pipe("xclip", []string{"-selection", "clipboard"}, text)
		}
		if _, err := exec.LookPath("xsel"); err == nil {
			return pipe("xsel", []string{"--clipboard", "--input"}, text)
		}
		return fmt.Errorf("no clipboard copy tool found; install wl-copy, xclip, or xsel")
	case "windows":
		return pipe("powershell.exe", []string{"-NoProfile", "-Command", "Set-Clipboard"}, text)
	default:
		return fmt.Errorf("clipboard copy unsupported on %s", clipboardGOOS)
	}
}

func ReadImagePNG() ([]byte, string, error) {
	switch clipboardGOOS {
	case "darwin":
		if _, err := exec.LookPath("pngpaste"); err != nil {
			return nil, "", fmt.Errorf("clipboard image support requires pngpaste: brew install pngpaste")
		}
		out, err := exec.Command("pngpaste", "-").Output()
		if err != nil {
			return nil, "", fmt.Errorf("no PNG image found on clipboard")
		}
		if len(out) == 0 {
			return nil, "", fmt.Errorf("no PNG image found on clipboard")
		}
		return out, "clipboard.png", nil
	case "linux":
		if _, err := exec.LookPath("wl-paste"); err == nil {
			out, err := exec.Command("wl-paste", "--type", "image/png").Output()
			if err == nil && len(out) > 0 {
				return out, "clipboard.png", nil
			}
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
			if err == nil && len(out) > 0 {
				return out, "clipboard.png", nil
			}
		}
		return nil, "", fmt.Errorf("no clipboard image found; install wl-paste or xclip")
	default:
		return nil, "", fmt.Errorf("clipboard image read unsupported on %s", clipboardGOOS)
	}
}

func pipe(name string, args []string, stdin string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewBufferString(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}
