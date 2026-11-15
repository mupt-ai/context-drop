package agents

import (
	"net/url"
	"testing"
)

func TestDesktopURL(t *testing.T) {
	link, err := desktopURL(Claude, "/work/my app", "fix the build & push")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if u.Scheme != "claude" || u.Host != "code" || u.Path != "/new" {
		t.Fatalf("claude link = %s", link)
	}
	if q := u.Query(); q.Get("folder") != "/work/my app" || q.Get("q") != "fix the build & push" {
		t.Fatalf("claude query = %v", q)
	}

	link, err = desktopURL(Codex, "/work/app", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(link)
	if u.Scheme != "codex" || u.Host != "threads" || u.Path != "/new" || u.Query().Get("path") != "/work/app" || u.Query().Has("prompt") {
		t.Fatalf("codex link = %s", link)
	}

	if _, err := desktopURL(Pi, "/work", ""); err == nil {
		t.Fatal("pi has no desktop app")
	}
}

func TestCodexDesktopProcess(t *testing.T) {
	if !codexDesktopProcess("/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex app-server") {
		t.Fatal("ChatGPT-hosted Codex not recognised")
	}
	if codexDesktopProcess("/opt/homebrew/bin/codex") {
		t.Fatal("terminal Codex taken for desktop")
	}
}
