package agents

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Desktop apps that host agent sessions. A session in one has no pane, so
// Context Drop names the app instead.
const (
	// MuxDesktop is the `start --in` value that opens the agent's desktop app.
	MuxDesktop = "desktop"

	AppClaudeDesktop = "claude-desktop"
	AppCodexDesktop  = "codex-desktop"
)

// AppLabel names a desktop app for display, e.g. "claude desktop".
func AppLabel(app string) string {
	return strings.ReplaceAll(app, "-", " ")
}

// codexDesktopProcess reports whether a Codex thread's process belongs to the
// Codex desktop app, which ships inside ChatGPT.app (and once as Codex.app).
func codexDesktopProcess(command string) bool {
	return strings.Contains(command, "/ChatGPT.app/") || strings.Contains(command, "/Codex.app/")
}

// desktopURL is the deep link that opens a new session in the agent's desktop
// app, in dir, with prompt typed into the composer. The apps fill the prompt
// in but do not send it; the person presses Enter.
func desktopURL(kind, dir, prompt string) (string, error) {
	q := url.Values{}
	switch kind {
	case Claude:
		q.Set("folder", dir)
		if prompt != "" {
			q.Set("q", prompt)
		}
		q.Set("source", "context-drop")
		return "claude://code/new?" + q.Encode(), nil
	case Codex:
		q.Set("path", dir)
		if prompt != "" {
			q.Set("prompt", prompt)
		}
		return "codex://threads/new?" + q.Encode(), nil
	}
	return "", fmt.Errorf("%s has no desktop app; --in desktop supports claude and codex", kind)
}

// DesktopApp is the app a kind's desktop sessions report.
func DesktopApp(kind string) string {
	if kind == Codex {
		return AppCodexDesktop
	}
	return AppClaudeDesktop
}

// LaunchDesktop opens a new session in the agent's desktop app and, when
// opts.Timeout is set, waits for it to appear. The app creates the session
// only once its first message is sent, so the wait covers the person
// pressing Enter on the filled-in prompt (or typing one).
func LaunchDesktop(ctx context.Context, newEnv func() (*Env, error), providers []Provider, opts LaunchOptions) (LaunchResult, error) {
	if !opts.At.IsZero() || len(opts.Args) > 0 || strings.TrimSpace(opts.Command) != "" {
		return LaunchResult{}, fmt.Errorf("--in desktop takes no --session, --window, --pane, --command, or agent arguments")
	}
	link, err := desktopURL(opts.Kind, opts.Dir, opts.Prompt)
	if err != nil {
		return LaunchResult{}, err
	}
	env, err := newEnv()
	if err != nil {
		return LaunchResult{}, err
	}
	provider, err := ProviderFor(providers, opts.Kind)
	if err != nil {
		return LaunchResult{}, err
	}
	before := map[string]bool{}
	for _, s := range Discover(ctx, env, []Provider{provider}).Sessions {
		before[s.ID] = true
	}
	app := DesktopApp(opts.Kind)
	launchedAt := time.Now()
	res := LaunchResult{Mux: MuxDesktop, Pane: app, Command: link, Status: LaunchStarted}
	if err := openURL(ctx, env, link, false); err != nil {
		return res, fmt.Errorf("open the %s app: %w", AppLabel(app), err)
	}
	if opts.Timeout <= 0 {
		return res, nil
	}
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second // the wait is for a person pressing Enter
	}
	dir := canonicalDir(opts.Dir)
	deadline := time.Now().Add(opts.Timeout)
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(opts.Interval):
		}
		env, err := newEnv() // a fresh Env: each one reads the process table once
		if err != nil {
			return res, err
		}
		var newest Session
		for _, s := range Discover(ctx, env, []Provider{provider}).Sessions {
			// Only a session that started after the launch counts, so an older
			// one that was missed by the first scan is never taken for it.
			if before[s.ID] || s.Subagent || s.App != app || s.StartedAt.Before(launchedAt.Add(-launchSlack)) || canonicalDir(s.Cwd) != dir {
				continue
			}
			if newest.ID == "" || s.StartedAt.After(newest.StartedAt) {
				newest = s
			}
		}
		if newest.ID != "" {
			res.Session, res.Registered, res.Status = newest, true, LaunchRegistered
			return res, nil
		}
		if time.Now().After(deadline) {
			res.Status = LaunchNotRegistered
			return res, nil
		}
	}
}

// openURL hands a link to macOS `open`; background keeps the app from coming forward.
func openURL(ctx context.Context, env *Env, link string, background bool) error {
	args := []string{link}
	if background {
		args = []string{"-g", link}
	}
	_, err := env.runEnv(ctx, nil, nil, env.Bin("open"), args...)
	return err
}
