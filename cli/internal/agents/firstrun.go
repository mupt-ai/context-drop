package agents

import (
	"regexp"
	"strings"
)

// First-run screens a new agent can stop at before it accepts messages.
const (
	// ScreenTrust is a prompt to trust the working directory.
	ScreenTrust = "trust"
	// ScreenSetup is a login, onboarding, or other first-run screen.
	ScreenSetup = "setup"
)

// firstRunLines is how much of the bottom of a pane is checked for a first-run screen.
const firstRunLines = 40

var (
	trustScreen = regexp.MustCompile(`(?i)do you trust the (files|contents)|trust this folder|project you created or one you trust|allow codex to work in this folder`)
	setupScreen = regexp.MustCompile(`(?i)select login method|choose the text style|sign in with chatgpt|do you want to use this api key|provide your own api key`)
	// A selected menu option: "❯ Yes, I trust this folder", "› 1. Yes, continue".
	selectedOption = regexp.MustCompile(`^\s*[❯›>]\s*(?:\d+\.\s*)?(.+)$`)
	yesOption      = regexp.MustCompile(`(?i)^(yes\b|trust\b)|\bi trust\b`)
	noOption       = regexp.MustCompile(`(?i)^(no\b|exit\b|quit\b)`)
)

// firstRunScreen reports which first-run screen, if any, the bottom of a pane shows.
func firstRunScreen(screen string) string {
	tail := lastLines(screen, firstRunLines)
	switch {
	case trustScreen.MatchString(tail):
		return ScreenTrust
	case setupScreen.MatchString(tail):
		return ScreenSetup
	}
	return ""
}

// trustKey returns the key that moves a folder-trust prompt towards trusting
// the folder: "enter" when the selected option trusts it, "down" when it
// declines, and "" when the menu cannot be read safely. Options that also
// waive approvals ("without asking for approval") are never chosen.
func trustKey(screen string) string {
	if firstRunScreen(screen) != ScreenTrust {
		return ""
	}
	lines := strings.Split(lastLines(screen, firstRunLines), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := selectedOption.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		option := strings.TrimSpace(m[1])
		switch {
		case strings.Contains(strings.ToLower(option), "approval"):
			return ""
		case yesOption.MatchString(option):
			return "enter"
		case noOption.MatchString(option):
			return "down"
		}
		return ""
	}
	return ""
}
