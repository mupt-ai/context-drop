// Package mods runs the commands an org adds to Context Drop. A mod is one TypeScript file that
// exports `mod({ name, description, input, can, on, run })`; the server keeps its versions and runs it
// in Deno with only the permissions its `can` list grants, calling back into the server for everything
// it does with the org.
package mods

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed js/mod.ts js/runner.ts
var jsFiles embed.FS

// Runtime runs mod source in Deno, one process per call.
type Runtime struct {
	deno string
	dir  string // holds mod.ts and runner.ts; each call gets a directory beneath it
	// MemoryMB caps the V8 heap of one call.
	MemoryMB int
}

// NewRuntime prepares a runtime. deno is the Deno executable; empty looks it up on PATH.
func NewRuntime(deno string) (*Runtime, error) {
	if deno == "" {
		found, err := exec.LookPath("deno")
		if err != nil {
			return nil, errors.New("deno is not installed; mods need it (set CONTEXT_DROP_DENO)")
		}
		deno = found
	}
	dir, err := os.MkdirTemp("", "context-drop-mods-")
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"mod.ts", "runner.ts"} {
		b, err := jsFiles.ReadFile("js/" + name)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return nil, err
		}
	}
	return &Runtime{deno: deno, dir: dir, MemoryMB: 256}, nil
}

// Library is the source of `@contextdrop/mod`, for editors and type checking.
func Library() []byte {
	b, _ := jsFiles.ReadFile("js/mod.ts")
	return b
}

// API is how a running mod reaches the server.
type API struct {
	Base  string `json:"base"`
	Org   string `json:"org"`
	Token string `json:"token"`
}

type invocation struct {
	Mode    string            `json:"mode"`
	Input   json.RawMessage   `json:"input,omitempty"`
	Event   any               `json:"event,omitempty"`
	Caller  any               `json:"caller,omitempty"`
	API     *API              `json:"api,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

type outcome struct {
	OK     bool            `json:"ok"`
	Output json.RawMessage `json:"output"`
	Error  string          `json:"error"`
}

// ErrMod is a failure inside the mod itself (a thrown error, bad input), as opposed to the runtime.
type ErrMod struct{ Message string }

func (e *ErrMod) Error() string { return e.Message }

// exec runs one call and returns the mod's output and what it printed.
func (r *Runtime) exec(ctx context.Context, source string, inv invocation, allowNet []string, timeout time.Duration) (json.RawMessage, string, error) {
	call, err := os.MkdirTemp(r.dir, "call-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(call)
	importMap := fmt.Sprintf(`{"imports":{"@contextdrop/mod":%q}}`, "file://"+filepath.Join(r.dir, "mod.ts"))
	entry := fmt.Sprintf("import def from \"./mod.ts\"\nimport { main } from %q\nawait main(def)\n", "file://"+filepath.Join(r.dir, "runner.ts"))
	for name, body := range map[string]string{"mod.ts": source, "entry.ts": entry, "import_map.json": importMap} {
		if err := os.WriteFile(filepath.Join(call, name), []byte(body), 0o600); err != nil {
			return nil, "", err
		}
	}
	args := []string{"run", "--quiet", "--no-prompt", "--no-config", "--no-remote", "--no-npm",
		"--import-map=" + filepath.Join(call, "import_map.json"),
		"--v8-flags=--max-old-space-size=" + strconv.Itoa(r.MemoryMB)}
	if len(allowNet) > 0 {
		args = append(args, "--allow-net="+strings.Join(allowNet, ","))
	}
	args = append(args, filepath.Join(call, "entry.ts"))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.deno, args...)
	cmd.Dir = call
	cmd.Env = []string{"NO_COLOR=1", "HOME=" + call, "DENO_DIR=" + filepath.Join(r.dir, "cache"), "PATH=/usr/bin:/bin"}
	stdin, _ := json.Marshal(inv)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	logs := tail(stderr.String(), 8<<10)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, logs, &ErrMod{Message: fmt.Sprintf("the mod ran longer than %s and was stopped", timeout)}
	}
	var res outcome
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		msg := strings.TrimSpace(logs)
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		return nil, logs, &ErrMod{Message: "the mod did not run: " + tail(msg, 2000)}
	}
	if !res.OK {
		return nil, logs, &ErrMod{Message: res.Error}
	}
	return res.Output, logs, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Meta is what a mod says about itself.
type Meta struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input"`
	Can         []string        `json:"can"`
	On          []string        `json:"on"`
	Webhook     *Webhook        `json:"webhook,omitempty"`
}

// Webhook is a mod's public URL and how its requests are verified.
type Webhook struct {
	Verify string `json:"verify"` // "github", "slack", or "hmac"
}

// Describe loads source without running it and returns its metadata.
func (r *Runtime) Describe(ctx context.Context, source string) (Meta, error) {
	out, _, err := r.exec(ctx, source, invocation{Mode: "describe"}, nil, 30*time.Second)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(out, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}
