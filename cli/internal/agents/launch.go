package agents

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// LaunchOptions describe a new agent to start in a multiplexer.
type LaunchOptions struct {
	Kind string
	Dir  string
	// Name labels the tab, window, or block.
	Name string
	// Mux is tmux, herdr, or rex; empty picks one with DefaultMux.
	Mux string
	// At names where in the multiplexer to open the agent's pane.
	At Placement
	// Command is the command line that starts the agent instead of its bare
	// executable, e.g. "dari claude". "{agent}" is replaced with Kind, so one
	// template ("dari {agent}") serves every agent. Args follow it.
	Command string
	Args    []string
	// Prompt is passed on the command line for agents that only create a
	// session once they receive one (OpenCode). Other agents get it with Send.
	Prompt string
	// Trust accepts a folder-trust prompt if the agent shows one.
	Trust bool
	// OnScreen is called when the agent stops at a first-run screen
	// (ScreenTrust or ScreenSetup) that Launch will not get past by itself.
	OnScreen func(screen string, at Location)
	// Timeout bounds the wait for the new session to appear. Zero skips the wait.
	Timeout  time.Duration
	Interval time.Duration
}

// Launch statuses.
const (
	// LaunchRegistered: the agent appears in `context-drop list` and accepts messages.
	LaunchRegistered = "registered"
	// LaunchWaitingForTrust: the agent is waiting for its folder to be trusted.
	LaunchWaitingForTrust = "waiting_for_trust"
	// LaunchWaitingForSetup: the agent is at a login or other first-run screen.
	LaunchWaitingForSetup = "waiting_for_setup"
	// LaunchNotRegistered: the agent did not appear before the timeout.
	LaunchNotRegistered = "not_registered"
	// LaunchStarted: Launch did not wait.
	LaunchStarted = "started"
)

var screenStatus = map[string]string{ScreenTrust: LaunchWaitingForTrust, ScreenSetup: LaunchWaitingForSetup}

// LaunchResult reports where the agent started and, once it registered, its session.
type LaunchResult struct {
	Mux        string   `json:"mux"`
	Pane       string   `json:"pane"`
	Location   Location `json:"location"`
	Command    string   `json:"command"`
	Status     string   `json:"status"`
	Registered bool     `json:"registered"`
	Session    Session  `json:"session,omitempty"`
}

// DefaultMux prefers the multiplexer the caller is running in, then whichever is installed.
func DefaultMux(env *Env) (string, error) {
	switch {
	case env.Getenv("TMUX") != "":
		return MuxTmux, nil
	case env.Getenv("HERDR_ENV") == "1":
		return MuxHerdr, nil
	case env.Getenv("REX_BLOCK") != "":
		return MuxRex, nil
	}
	if _, err := exec.LookPath(env.Bin("tmux")); err == nil {
		return MuxTmux, nil
	}
	if _, err := exec.LookPath(env.Bin("herdr")); err == nil {
		return MuxHerdr, nil
	}
	if _, err := exec.LookPath(env.rexBin()); err == nil {
		return MuxRex, nil
	}
	return "", fmt.Errorf("no tmux, herdr, or rex found; install one or pass --in")
}

// agentArgv builds the command that starts an agent.
func agentArgv(env *Env, opts LaunchOptions) ([]string, error) {
	switch opts.Kind {
	case Claude, Codex, Pi, OpenCode:
	default:
		return nil, fmt.Errorf("unsupported agent %q (want claude, codex, pi, or opencode)", opts.Kind)
	}
	argv := []string{env.Bin(opts.Kind)}
	if strings.TrimSpace(opts.Command) != "" {
		words, err := shellSplit(strings.ReplaceAll(opts.Command, "{agent}", opts.Kind))
		if err != nil {
			return nil, fmt.Errorf("launch command: %w", err)
		}
		if len(words) == 0 {
			return nil, fmt.Errorf("launch command is empty")
		}
		argv = words
	}
	argv = append(argv, opts.Args...)
	if opts.Kind == OpenCode && opts.Prompt != "" {
		argv = append(argv, "--prompt", opts.Prompt)
	}
	return argv, nil
}

// maxTrustKeys bounds the keys pressed to get through a trust prompt.
const maxTrustKeys = 4

// Launch starts an agent in a new pane (a tmux window, herdr tab, or rex
// block, or a split beside opts.At) and, when opts.Timeout is set, waits for
// its session to appear in discovery. While it waits it watches the pane for
// first-run screens: a folder-trust prompt is accepted when opts.Trust is
// set, and any other screen is reported through opts.OnScreen and in the
// result's Status.
func Launch(ctx context.Context, newEnv func() (*Env, error), providers []Provider, opts LaunchOptions) (LaunchResult, error) {
	env, err := newEnv()
	if err != nil {
		return LaunchResult{}, err
	}
	if opts.Mux == "" {
		if opts.Mux, err = DefaultMux(env); err != nil {
			return LaunchResult{}, err
		}
	}
	mux, err := muxFor(opts.Mux)
	if err != nil {
		return LaunchResult{}, err
	}
	argv, err := agentArgv(env, opts)
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
	launchedAt := time.Now()

	res := LaunchResult{Mux: opts.Mux, Command: shellJoin(argv), Status: LaunchStarted}
	res.Location, err = mux.open(ctx, env, opts.At, opts.Dir, opts.Name, argv)
	res.Pane = res.Location.Pane
	if err != nil || opts.Timeout <= 0 {
		return res, err
	}

	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	dir := canonicalDir(opts.Dir)
	deadline := time.Now().Add(opts.Timeout)
	reported, keys := "", 0
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(opts.Interval):
		}
		env, err := newEnv()
		if err != nil {
			return res, err
		}
		if !res.Registered {
			var fresh []Session
			for _, s := range Discover(ctx, env, []Provider{provider}).Sessions {
				if !before[s.ID] && !s.Subagent && canonicalDir(s.Cwd) == dir {
					fresh = append(fresh, s)
				}
			}
			if s, ok := pickLaunched(ctx, env, mux, fresh, res.Location, launchedAt); ok {
				res.Session, res.Registered = s, true
			}
		}
		screen, captureErr := mux.capture(ctx, env, res.Location, firstRunLines)
		waiting := firstRunScreen(screen)
		if waiting == ScreenTrust && opts.Trust && keys < maxTrustKeys {
			if key := trustKey(screen); key != "" {
				if err := mux.press(ctx, env, res.Location, key); err != nil {
					return res, fmt.Errorf("accept the trust prompt: %w", err)
				}
				keys++
				continue
			}
		}
		switch {
		case waiting != "":
			res.Status = screenStatus[waiting]
			if waiting != reported && opts.OnScreen != nil {
				opts.OnScreen(waiting, res.Location)
			}
			reported = waiting
		case res.Registered && captureErr == nil:
			res.Status = LaunchRegistered
			return res, nil
		case res.Registered:
			// The pane could not be read, so a dialog cannot be ruled out yet;
			// keep checking, and settle for registered at the deadline.
			res.Status = LaunchRegistered
		default:
			res.Status = LaunchNotRegistered
		}
		if time.Now().After(deadline) {
			return res, nil
		}
	}
}

// launchSlack allows for clock and timestamp granularity around the launch time.
const launchSlack = 5 * time.Second

// pickLaunched finds the launched agent among sessions that are new since the
// launch. A session running in the launched pane wins. Otherwise only a
// session that started after the launch counts, so an older session that
// merely became visible (for example because a new client opened in its
// directory) is never mistaken for it. With no start time to go on, a single
// new session is accepted and anything more is left until the pane decides.
func pickLaunched(ctx context.Context, env *Env, mux multiplexer, fresh []Session, want Location, launchedAt time.Time) (Session, bool) {
	for _, s := range fresh {
		if s.PID <= 0 {
			continue
		}
		if loc, ok := mux.locate(ctx, env, s.PID, s.TTY); ok && loc.Pane == want.Pane {
			return s, true
		}
	}
	var started, undated []Session
	for _, s := range fresh {
		switch {
		case s.StartedAt.IsZero():
			undated = append(undated, s)
		case !s.StartedAt.Before(launchedAt.Add(-launchSlack)):
			started = append(started, s)
		}
	}
	if len(started) > 0 {
		sort.SliceStable(started, func(i, j int) bool { return started[i].StartedAt.After(started[j].StartedAt) })
		return started[0], true
	}
	if len(undated) == 1 && len(fresh) == 1 {
		return undated[0], true
	}
	return Session{}, false
}

// shellSplit splits a command line into words, honouring single quotes,
// double quotes, and backslash escapes. It does not expand anything.
func shellSplit(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord, quote, escaped := false, rune(0), false
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'' && r == '\'':
			quote = 0
		case quote == '\'':
			cur.WriteRune(r)
		case r == '\\':
			escaped, inWord = true, true
		case quote == '"' && r == '"':
			quote = 0
		case quote == '"':
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unterminated quote or escape in %q", line)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// shellJoin quotes argv for a POSIX shell.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r))
		}) < 0 {
			quoted[i] = a
		} else {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
