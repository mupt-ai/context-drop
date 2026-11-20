package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/orgserver"
	"contextdrop.dev/context-drop/internal/pool"
)

const greetMod = `import { mod, z } from "@contextdrop/mod"
export default mod({
  name: "greet",
  description: "Greets people, as a table",
  input: z.object({ who: z.array(z.string()).describe("names"), loud: z.boolean().default(false), times: z.number().default(1) }),
  run({ who, loud, times }) {
    return who.map(w => ({ name: w, greeting: (loud ? "HI " : "hi ") + w, times }))
  },
})`

// joinModOrg starts a server with mods on and joins this machine to it as laptop.
func joinModOrg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("deno is not installed")
	}
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	store, err := orgserver.OpenStore(filepath.Join(t.TempDir(), "orgs.json"))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := store.CreateOrg("acme")
	srv := orgserver.New(store, "", nil)
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	rt, err := mods.NewRuntime("")
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := mods.OpenStore(filepath.Join(t.TempDir(), "mods"))
	srv.Mods = mods.NewService(ms, rt, hs.URL, nil)
	if _, _, err := executeRoot(t, "join", "--server", hs.URL, "--name", "laptop", token); err != nil {
		t.Fatal(err)
	}
}

func TestDeployThenCallAModLikeACommand(t *testing.T) {
	joinModOrg(t)
	file := filepath.Join(t.TempDir(), "greet.ts")
	os.WriteFile(file, []byte(greetMod), 0o600)
	out, _, err := executeRoot(t, "deploy", file)
	if err != nil || !strings.Contains(out, "Deployed greet (can: nothing)") {
		t.Fatalf("deploy: %q, %v", out, err)
	}
	out, _, err = executeRoot(t, "mods")
	if err != nil || !strings.Contains(out, "greet") || !strings.Contains(out, "Greets people") {
		t.Fatalf("mods: %q, %v", out, err)
	}
	// Flags are coerced to the input's types; a list of objects prints as a table.
	out, _, err = executeRoot(t, "call", "greet", "--who", "ada", "--who=bob", "--loud", "--times", "2")
	if err != nil || !strings.Contains(out, "NAME") || !strings.Contains(out, "HI bob") || !strings.Contains(out, "  2") {
		t.Fatalf("call: %q, %v", out, err)
	}
	if _, _, err := executeRoot(t, "call", "greet", "--nope", "x"); err == nil || !strings.Contains(err.Error(), "unknown flag --nope") {
		t.Fatalf("unknown flag: %v", err)
	}
	out, _, err = executeRoot(t, "call", "greet", "--json", `{"who":["cy"]}`, "--print-json")
	if err != nil || strings.TrimSpace(out) != `[{"name":"cy","greeting":"hi cy","times":1}]` {
		t.Fatalf("--json: %q, %v", out, err)
	}
	out, _, err = executeRoot(t, "greet", "--help")
	if err == nil {
		t.Fatalf("an unknown command should fail without the rewrite: %q", out)
	}
	root := NewRootCommand(BuildInfo{})
	if got := ModCallArgs(root, []string{"greet", "--who", "ada"}); strings.Join(got, " ") != "call greet --who ada" {
		t.Fatalf("rewrite = %v", got)
	}
	if got := ModCallArgs(root, []string{"list", "--json"}); got[0] != "list" {
		t.Fatalf("a built-in was rewritten: %v", got)
	}
}

func TestMCPServesBuiltinsAndMods(t *testing.T) {
	joinModOrg(t)
	m, _ := membership()
	c := pool.Client{Membership: m}
	if err := c.Post(context.Background(), "mods/deploy", map[string]any{"source": greetMod}, nil); err != nil {
		t.Fatal(err)
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"who":["ada"]}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	srv := &mcpServer{client: c, out: &out}
	if err := srv.serve(context.Background(), strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(out.String(), "\n") < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	byID := map[string]json.RawMessage{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		json.Unmarshal([]byte(line), &msg)
		byID[string(msg.ID)] = msg.Result
	}
	if !strings.Contains(string(byID["1"]), `"listChanged":true`) {
		t.Fatalf("initialize = %s", byID["1"])
	}
	for _, tool := range []string{`"name":"list"`, `"name":"read"`, `"name":"send"`, `"name":"greet"`} {
		if !strings.Contains(string(byID["2"]), tool) {
			t.Fatalf("tools/list lacks %s: %s", tool, byID["2"])
		}
	}
	if !strings.Contains(string(byID["3"]), `hi ada`) {
		t.Fatalf("tools/call = %s", byID["3"])
	}
}
