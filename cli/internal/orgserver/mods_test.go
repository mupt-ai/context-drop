package orgserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/pool"
)

// withMods turns mods on for the org's server; mods need Deno.
func withMods(t *testing.T) *org {
	t.Helper()
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("deno is not installed")
	}
	o := newOrg(t)
	rt, err := mods.NewRuntime("")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := mods.OpenStore(filepath.Join(t.TempDir(), "mods"))
	if err != nil {
		t.Fatal(err)
	}
	o.server.Mods = mods.NewService(ms, rt, o.srv.URL, nil)
	o.server.Mods.Timeout = 20 * time.Second
	return o
}

func deploy(t *testing.T, c pool.Client, source string) (mods.Summary, error) {
	t.Helper()
	var res mods.Summary
	err := c.Post(context.Background(), "mods/deploy", map[string]any{"source": source}, &res)
	return res, err
}

func call(c pool.Client, name string, input any) (json.RawMessage, error) {
	var out struct {
		Output json.RawMessage `json:"output"`
	}
	err := c.Post(context.Background(), "mods/"+name+"/call", map[string]any{"input": input}, &out)
	return out.Output, err
}

const stuckMod = `import { mod, z } from "@contextdrop/mod"
export default mod({
  name: "stuck",
  description: "Agents not working",
  input: z.object({ status: z.string().default("idle") }),
  can: ["list"],
  async run({ status }, cd) {
    const agents = await cd.list()
    return agents.filter(a => a.status === status).map(a => ({ id: a.id, cwd: a.cwd }))
  },
})`

func TestModsAreEndpointsWithScopedCapabilities(t *testing.T) {
	o := withMods(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{
		agent("laptop", "cc-00000001", "/work/app", "main", "", "2026-11-17T10:00:00Z"),
	}}
	c := o.join(laptop)
	ctx := context.Background()
	waitFor(t, "laptop to report", func() bool { l, _ := c.List(ctx, pool.Selector{}); return len(l) == 1 })

	res, err := deploy(t, c, stuckMod)
	if err != nil || res.Name != "stuck" || len(res.Can) != 1 {
		t.Fatalf("deploy = %+v, %v", res, err)
	}
	out, err := call(c, "stuck", map[string]any{})
	if err != nil || !strings.Contains(string(out), `"id":"laptop/cc-00000001"`) {
		t.Fatalf("stuck = %s, %v", out, err)
	}
	if out, err := call(c, "stuck", map[string]any{"status": 3}); code(err) != "mod_failed" || !strings.Contains(err.Error(), "input.status must be a string") {
		t.Fatalf("bad input = %s, %v", out, err)
	}

	// A mod can use only what its can list grants.
	sneaky := `import { mod } from "@contextdrop/mod"
export default mod({ name: "sneaky", description: "", can: ["list"], async run(_, cd) { return await cd.send("laptop/cc-00000001", "rm -rf /") } })`
	if _, err := deploy(t, c, sneaky); err != nil {
		t.Fatal(err)
	}
	if _, err := call(c, "sneaky", nil); err == nil || !strings.Contains(err.Error(), `needs "send"`) || len(laptop.sent) != 0 {
		t.Fatalf("send without the capability: %v, sent %v", err, laptop.sent)
	}
	offline := `import { mod } from "@contextdrop/mod"
export default mod({ name: "exfil", description: "", async run() { return (await fetch("https://example.com")).status } })`
	if _, err := deploy(t, c, offline); err != nil {
		t.Fatal(err)
	}
	if _, err := call(c, "exfil", nil); err == nil || !strings.Contains(err.Error(), "net access") {
		t.Fatalf("fetch without net:<host>: %v", err)
	}
	if _, err := deploy(t, c, strings.Replace(stuckMod, `["list"]`, `["net:example.com"]`, 1)); code(err) != "bad_request" {
		t.Fatalf("a capability mods do not have: %v", err)
	}

	// Mods use the same endpoints with a token that is only good while they run.
	var list []mods.Summary
	if err := c.Post(ctx, "mods", struct{}{}, &list); err != nil || len(list) != 3 || list[2].Name != "stuck" || list[2].Author != "laptop" {
		t.Fatalf("mods = %+v, %v", list, err)
	}
}

func TestModKeyValueSurvivesARedeploy(t *testing.T) {
	o := withMods(t)
	laptop := o.join(&fakeMachine{name: "laptop"})
	devbox := o.join(&fakeMachine{name: "devbox"})
	counter := func(step int) string {
		return `import { mod } from "@contextdrop/mod"
export default mod({ name: "counter", description: "counts", can: ["kv"], async run(_, cd) {
  for (;;) {
    const n = (await cd.kv.get("n")) ?? 0
    if (await cd.kv.cas("n", n === 0 ? undefined : n, n + ` + strconv.Itoa(step) + `)) return n + ` + strconv.Itoa(step) + `
  }
} })`
	}
	if _, err := deploy(t, laptop, counter(1)); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		if out, err := call(devbox, "counter", nil); err != nil || string(out) != strconv.Itoa(want) {
			t.Fatalf("counter call %d = %s, %v", want, out, err)
		}
	}
	// Deploying again replaces the mod; its data stays.
	if _, err := deploy(t, devbox, counter(10)); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(laptop, "counter", nil); string(out) != "13" {
		t.Fatalf("after redeploy = %s", out)
	}
	var list []mods.Summary
	if err := laptop.Post(context.Background(), "mods", struct{}{}, &list); err != nil || len(list) != 1 || list[0].Author != "devbox" {
		t.Fatalf("mods = %+v, %v", list, err)
	}
}

func TestReferenceModsDeployAndLockAcrossMachines(t *testing.T) {
	o := withMods(t)
	laptop := o.join(&fakeMachine{name: "laptop"})
	devbox := o.join(&fakeMachine{name: "devbox"})
	for _, f := range []string{"stuck.ts", "lock.ts"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "..", "mods", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deploy(t, laptop, string(src)); err != nil {
			t.Fatalf("deploy %s: %v", f, err)
		}
	}
	if out, err := call(laptop, "stuck", nil); err != nil || string(out) != "[]" {
		t.Fatalf("stuck = %s, %v", out, err)
	}
	if out, err := call(laptop, "lock", map[string]any{"path": "src/parser", "note": "tokenizer"}); err != nil || string(out) != `"laptop holds src/parser"` {
		t.Fatalf("lock = %s, %v", out, err)
	}
	if _, err := call(devbox, "lock", map[string]any{"path": "src/parser"}); err == nil || !strings.Contains(err.Error(), "held by laptop") {
		t.Fatalf("second lock: %v", err)
	}
	if out, err := call(laptop, "lock", map[string]any{"path": "src/parser", "release": true}); err != nil || string(out) != `"released src/parser"` {
		t.Fatalf("release = %s, %v", out, err)
	}
	if _, err := call(devbox, "lock", map[string]any{"path": "src/parser"}); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if out, _ := call(laptop, "lock", nil); !strings.Contains(string(out), `"by":"devbox"`) {
		t.Fatalf("held locks = %s", out)
	}
}
