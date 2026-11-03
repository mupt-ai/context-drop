package agents

import (
	"context"
	"encoding/json"
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
	// Command is the command line that starts the agent instead of its bare
	// executable, e.g. "dari claude". "{agent}" is replaced with Kind, so one
	// template ("dari {agent}") serves every agent. Args follow it.
	Command string
	Args    []string
	// Prompt is passed on the command line for agents that only create a
	// session once they receive one (OpenCode). Other agents get it with Send.
	Prompt string
	// Timeout bounds the wait for the new session to appear. Zero skips the wait.
	Timeout  time.Duration
	Interval time.Duration
}

// LaunchResult reports where the agent started and, once it registered, its session.
type LaunchResult struct {
	Mux        string  `json:"mux"`
	Pane       string  `json:"pane"`
	Command    string  `json:"command"`
	Registered bool    `json:"registered"`
	Session    Session `json:"session,omitempty"`
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

// Launch starts an agent in a new tmux window, herdr tab, or rex block and,
// when opts.Timeout is set, waits for its session to appear in discovery.
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

	res := LaunchResult{Mux: opts.Mux, Command: shellJoin(argv)}
	switch opts.Mux {
	case MuxTmux:
		res.Pane, err = launchTmux(ctx, env, opts, argv)
	case MuxHerdr:
		res.Pane, err = launchHerdr(ctx, env, opts, argv)
	case MuxRex:
		res.Pane, err = launchRex(ctx, env, opts, argv)
	default:
		err = fmt.Errorf("unknown multiplexer %q (want tmux, herdr, or rex)", opts.Mux)
	}
	if err != nil || opts.Timeout <= 0 {
		return res, err
	}

	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	want := paneRef{opts.Mux, res.Pane}
	dir := canonicalDir(opts.Dir)
	deadline := time.Now().Add(opts.Timeout)
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
		var fresh []Session
		for _, s := range Discover(ctx, env, []Provider{provider}).Sessions {
			if !before[s.ID] && !s.Subagent && canonicalDir(s.Cwd) == dir {
				fresh = append(fresh, s)
			}
		}
		if s, ok := pickLaunched(ctx, env, fresh, want, launchedAt); ok {
			res.Session, res.Registered = s, true
			return res, nil
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
func pickLaunched(ctx context.Context, env *Env, fresh []Session, want paneRef, launchedAt time.Time) (Session, bool) {
	for _, s := range fresh {
		if s.PID > 0 && resolvePane(ctx, env, s.PID, s.TTY) == want {
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

func launchTmux(ctx context.Context, env *Env, opts LaunchOptions, argv []string) (string, error) {
	tmux := env.Bin("tmux")
	window := []string{"-n", opts.Name, "-c", opts.Dir, "-P", "-F", "#{pane_id}", "--"}
	out, err := env.Run(ctx, nil, tmux, append(append([]string{"new-window", "-t", "context-drop:"}, window...), argv...)...)
	if err != nil {
		// No context-drop session yet: create it with the agent as its first window.
		out, err = env.Run(ctx, nil, tmux, append(append([]string{"new-session", "-d", "-s", "context-drop"}, window...), argv...)...)
		if err != nil {
			return "", err
		}
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		return "", fmt.Errorf("tmux did not report the new pane")
	}
	return pane, nil
}

func launchHerdr(ctx context.Context, env *Env, opts LaunchOptions, argv []string) (string, error) {
	herdr := env.Bin("herdr")
	out, err := env.Run(ctx, nil, herdr, "tab", "create", "--cwd", opts.Dir, "--label", opts.Name)
	if err != nil {
		return "", err
	}
	var created struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &created) != nil || created.Result.RootPane.PaneID == "" {
		return "", fmt.Errorf("herdr did not report the new pane: %s", oneLine(string(out), 200))
	}
	pane := created.Result.RootPane.PaneID
	// The tab starts a shell; run the agent in it so the user's PATH and profile apply.
	if _, err := env.Run(ctx, nil, herdr, "pane", "run", pane, shellJoin(argv)); err != nil {
		return pane, err
	}
	return pane, nil
}

func launchRex(ctx context.Context, env *Env, opts LaunchOptions, argv []string) (string, error) {
	args := append([]string{"run", "--cwd", opts.Dir, "--label", opts.Name, "--json", "--"}, argv...)
	out, err := env.Run(ctx, nil, env.rexBin(), args...)
	if err != nil {
		return "", err
	}
	var created struct {
		BlockIDs []string `json:"block_ids"`
	}
	if json.Unmarshal(out, &created) != nil || len(created.BlockIDs) == 0 {
		return "", fmt.Errorf("rex did not report the new block: %s", oneLine(string(out), 200))
	}
	return created.BlockIDs[0], nil
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
