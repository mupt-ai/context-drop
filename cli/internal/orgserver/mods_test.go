package orgserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
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

func deploy(t *testing.T, c pool.Client, source string, preview bool) (mods.DeployResult, error) {
	t.Helper()
	var res mods.DeployResult
	err := c.Post(context.Background(), "mods/deploy", map[string]any{"source": source, "preview": preview}, &res)
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

	res, err := deploy(t, c, stuckMod, false)
	if err != nil || res.Name != "stuck" || res.Version != 1 || len(res.Added) != 1 {
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
	if _, err := deploy(t, c, sneaky, false); err != nil {
		t.Fatal(err)
	}
	if _, err := call(c, "sneaky", nil); err == nil || !strings.Contains(err.Error(), `needs "send"`) || len(laptop.sent) != 0 {
		t.Fatalf("send without the capability: %v, sent %v", err, laptop.sent)
	}
	offline := `import { mod } from "@contextdrop/mod"
export default mod({ name: "exfil", description: "", async run() { return (await fetch("https://example.com")).status } })`
	if _, err := deploy(t, c, offline, false); err != nil {
		t.Fatal(err)
	}
	if _, err := call(c, "exfil", nil); err == nil || !strings.Contains(err.Error(), "net access") {
		t.Fatalf("fetch without net:<host>: %v", err)
	}
	if _, err := deploy(t, c, strings.Replace(stuckMod, `["list"]`, `["ask"]`, 1), false); code(err) != "unsupported" {
		t.Fatalf("a capability this server lacks: %v", err)
	}

	// Mods use the same endpoints with a token that is only good while they run.
	var list []mods.Summary
	if err := c.Post(ctx, "mods", struct{}{}, &list); err != nil || len(list) != 3 || list[2].Name != "stuck" || list[2].Author != "laptop" {
		t.Fatalf("mods = %+v, %v", list, err)
	}
}

func TestModVersionsPreviewsAndKeyValue(t *testing.T) {
	o := withMods(t)
	laptop := o.join(&fakeMachine{name: "laptop"})
	devbox := o.join(&fakeMachine{name: "devbox"})
	counter := func(step int) string {
		return `import { mod } from "@contextdrop/mod"
export default mod({ name: "counter", description: "counts", can: ["kv"], async run(_, cd) {
  for (;;) {
    const n = (await cd.kv.get("n")) ?? 0
    if (await cd.kv.cas("n", n === 0 ? undefined : n, n + ` + strings.Repeat("1", step) + `)) return n + ` + strings.Repeat("1", step) + `
  }
} })`
	}
	if _, err := deploy(t, laptop, counter(1), false); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		if out, err := call(devbox, "counter", nil); err != nil || string(out) != strconv.Itoa(want) {
			t.Fatalf("counter call %d = %s, %v", want, out, err)
		}
	}
	// v2 counts by 11; rollback restores v1, and the data stays.
	if res, err := deploy(t, laptop, counter(2), false); err != nil || res.Version != 2 || len(res.Added) != 0 {
		t.Fatalf("v2 = %+v, %v", res, err)
	}
	if out, _ := call(devbox, "counter", nil); string(out) != "14" {
		t.Fatalf("v2 call = %s", out)
	}
	if err := laptop.Post(context.Background(), "mods/counter/rollback", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(devbox, "counter", nil); string(out) != "15" {
		t.Fatalf("after rollback = %s", out)
	}

	// A preview is name@machine, and only that machine can see or call it.
	res, err := deploy(t, devbox, counter(1), true)
	if err != nil || res.Name != "counter@devbox" {
		t.Fatalf("preview = %+v, %v", res, err)
	}
	if _, err := call(laptop, "counter@devbox", nil); code(err) != "not_found" {
		t.Fatalf("another machine's preview: %v", err)
	}
	if out, err := call(devbox, "counter@devbox", nil); err != nil || string(out) != "1" {
		t.Fatalf("own preview = %s, %v", out, err)
	}
}

func TestModEventsAndWebhooks(t *testing.T) {
	o := withMods(t)
	laptop := &fakeMachine{name: "laptop", agents: []pool.Agent{agent("laptop", "cc-00000001", "/w", "main", "", "")}}
	c := o.join(laptop)
	ctx := context.Background()
	waitFor(t, "laptop to report", func() bool { l, _ := c.List(ctx, pool.Selector{}); return len(l) == 1 })

	watcher := `import { mod } from "@contextdrop/mod"
export default mod({ name: "arrivals", description: "", can: ["kv"], on: ["agent.started"], async run(_, cd, event) {
  if (event) { await cd.kv.set("last", event.agent.id); return }
  return await cd.kv.get("last")
} })`
	if _, err := deploy(t, c, watcher, false); err != nil {
		t.Fatal(err)
	}
	laptop.mu.Lock()
	laptop.agents = append(laptop.agents, agent("laptop", "cc-00000002", "/w", "main", "", ""))
	laptop.mu.Unlock()
	waitFor(t, "the agent.started event", func() bool {
		out, _ := call(c, "arrivals", nil)
		return string(out) == `"laptop/cc-00000002"`
	})

	hook := `import { mod } from "@contextdrop/mod"
export default mod({ name: "deploys", description: "", on: ["webhook"], webhook: { verify: "hmac" },
  run(_, cd, event) { return "got " + event.body.ref } })`
	res, err := deploy(t, c, hook, false)
	if err != nil || res.Webhook != "/hooks/acme/deploys" {
		t.Fatalf("deploy hook = %+v, %v", res, err)
	}
	post := func(sig string) (int, string) {
		body := []byte(`{"ref":"main"}`)
		req, _ := http.NewRequest("POST", o.srv.URL+res.Webhook, bytes.NewReader(body))
		req.Header.Set("X-Signature-256", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	if status, _ := post("sha256=00"); status != http.StatusServiceUnavailable {
		t.Fatalf("hook without a secret: %d", status)
	}
	if err := c.Post(ctx, "mods/deploys/secret", map[string]string{"key": "webhook", "value": "s3cret"}, nil); err != nil {
		t.Fatal(err)
	}
	if status, _ := post("sha256=00"); status != http.StatusUnauthorized {
		t.Fatalf("hook with a bad signature: %d", status)
	}
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write([]byte(`{"ref":"main"}`))
	if status, body := post("sha256=" + hex.EncodeToString(m.Sum(nil))); status != http.StatusOK || body != "got main" {
		t.Fatalf("signed hook = %d %q", status, body)
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
		if _, err := deploy(t, laptop, string(src), false); err != nil {
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
