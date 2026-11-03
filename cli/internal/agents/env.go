package agents

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Process is one row of the process table.
type Process struct {
	PID     int
	PPID    int
	TTY     string
	Start   time.Time
	Command string
}

// Runner executes an external command and returns its stdout.
type Runner func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)

// Env holds everything discovery touches on the host so tests can replace it.
type Env struct {
	Home     string
	Hostname string
	Run      Runner
	// RunEnv is Run with extra environment variables (KEY=VALUE).
	RunEnv func(ctx context.Context, extraEnv []string, stdin []byte, name string, args ...string) ([]byte, error)
	// Processes returns the live process table, keyed by PID.
	Processes func(ctx context.Context) (map[int]Process, error)
	// OpenFiles returns, for each given path, the PIDs that hold it open.
	OpenFiles func(ctx context.Context, paths []string) (map[string][]int, error)
	// Cwds returns the working directories of the given PIDs.
	Cwds func(ctx context.Context, pids []int) (map[int]string, error)
	// Getenv reads configuration such as XDG_DATA_HOME and binary overrides.
	Getenv func(string) string

	procOnce sync.Once
	procs    map[int]Process
	procErr  error

	tmuxOnce  sync.Once
	tmuxPanes map[string]string

	herdrOnce sync.Once
	herdr     map[int]string
}

// tmuxPane returns the tmux pane attached to tty, if tmux is running.
func (e *Env) tmuxPane(ctx context.Context, tty string) string {
	if tty == "" {
		return ""
	}
	e.tmuxOnce.Do(func() {
		e.tmuxPanes = map[string]string{}
		out, err := e.Run(ctx, nil, e.Bin("tmux"), "list-panes", "-a", "-F", "#{pane_id} #{pane_tty}")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			if id, paneTTY, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
				e.tmuxPanes[paneTTY] = id
			}
		}
	})
	return e.tmuxPanes[tty]
}

// NewEnv returns an Env bound to the real machine.
func NewEnv() (*Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &Env{
		Home:      home,
		Hostname:  host,
		Run:       execRunner,
		RunEnv:    execRunnerEnv,
		Processes: psProcesses,
		OpenFiles: lsofOpenFiles,
		Cwds:      lsofCwds,
		Getenv:    os.Getenv,
	}, nil
}

// Procs returns the process table, read once per Env.
func (e *Env) Procs(ctx context.Context) (map[int]Process, error) {
	e.procOnce.Do(func() { e.procs, e.procErr = e.Processes(ctx) })
	return e.procs, e.procErr
}

// Alive reports whether pid is in the process table.
func (e *Env) Alive(ctx context.Context, pid int) (Process, bool) {
	if pid <= 0 {
		return Process{}, false
	}
	procs, err := e.Procs(ctx)
	if err != nil {
		return Process{}, false
	}
	p, ok := procs[pid]
	return p, ok
}

// Bin returns the executable for an agent CLI, honouring CONTEXT_DROP_<NAME>_BIN.
func (e *Env) Bin(name string) string {
	if v := e.Getenv("CONTEXT_DROP_" + strings.ToUpper(name) + "_BIN"); v != "" {
		return v
	}
	return name
}

func (e *Env) path(parts ...string) string {
	return filepath.Join(append([]string{e.Home}, parts...)...)
}

// ContextDropHome is where Context Drop keeps its own state.
func (e *Env) ContextDropHome() string {
	if v := e.Getenv("CONTEXT_DROP_HOME"); v != "" {
		return v
	}
	return e.path(".context-drop")
}

func execRunner(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return execRunnerEnv(ctx, nil, stdin, name, args...)
}

func execRunnerEnv(ctx context.Context, extraEnv []string, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg != "" {
			return out, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, oneLine(msg, 400))
		}
		return out, fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return out, nil
}

func psProcesses(ctx context.Context) (map[int]Process, error) {
	out, err := execRunner(ctx, nil, "ps", "-axww", "-o", "pid=,ppid=,tty=,lstart=,command=")
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

// parsePS parses `ps -o pid=,ppid=,tty=,lstart=,command=` output. lstart is
// always five fields, e.g. "Mon Nov  2 21:24:19 2026".
func parsePS(out []byte) map[int]Process {
	procs := map[int]Process{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 8 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		start, _ := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[3:8], " "), time.Local)
		tty := f[2]
		if tty == "??" || tty == "?" || tty == "-" {
			tty = ""
		} else if !strings.HasPrefix(tty, "/dev/") {
			tty = "/dev/" + tty
		}
		procs[pid] = Process{PID: pid, PPID: ppid, TTY: tty, Start: start, Command: strings.Join(f[8:], " ")}
	}
	return procs
}

func lsofOpenFiles(ctx context.Context, paths []string) (map[string][]int, error) {
	result := map[string][]int{}
	if len(paths) == 0 {
		return result, nil
	}
	args := append([]string{"-n", "-w", "-F", "pn", "--"}, paths...)
	out, err := execRunner(ctx, nil, "lsof", args...)
	if err != nil && len(out) == 0 {
		// lsof exits 1 when none of the files is open.
		return result, nil
	}
	return parseLsof(out), nil
}

func parseLsof(out []byte) map[string][]int {
	result := map[string][]int{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			name := line[1:]
			if pid > 0 && !containsInt(result[name], pid) {
				result[name] = append(result[name], pid)
			}
		}
	}
	return result
}

func lsofCwds(ctx context.Context, pids []int) (map[int]string, error) {
	result := map[int]string{}
	if len(pids) == 0 {
		return result, nil
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	out, err := execRunner(ctx, nil, "lsof", "-n", "-w", "-a", "-d", "cwd", "-F", "pn", "-p", strings.Join(list, ","))
	if err != nil && len(out) == 0 {
		return result, nil
	}
	for name, holders := range parseLsof(out) {
		for _, p := range holders {
			result[p] = name
		}
	}
	return result, nil
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// commandBase returns the executable name of a process command line.
func commandBase(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	return filepath.Base(f[0])
}

func (e *Env) runEnv(ctx context.Context, extraEnv []string, stdin []byte, name string, args ...string) ([]byte, error) {
	if e.RunEnv != nil {
		return e.RunEnv(ctx, extraEnv, stdin, name, args...)
	}
	return e.Run(ctx, stdin, name, args...)
}
