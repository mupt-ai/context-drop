import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildAgentArgv, type CommandRunner } from "../src/launch.js";
import { launchInHerdr } from "../src/herdr.js";
import { launchInTmux } from "../src/tmux.js";
import { createRuntimeServer } from "../src/server.js";
import type { RuntimeConfig } from "../src/types.js";

const config = (): RuntimeConfig => ({ host: "127.0.0.1", port: 0, stateDir: mkdtempSync(join(tmpdir(), "cd-runtime-")), tokenFile: "token", tmuxSession: "context-drop", herdrPath: "herdr", herdrSession: "default", agents: { mock: { command: ["mock-agent", "--prompt-file", "{prompt_file}"] } } });

test("buildAgentArgv preserves argv boundaries", () => {
  assert.deepEqual(buildAgentArgv({ command: ["agent", "--file", "{prompt_file}"] }, "/tmp/a b;rm"), ["agent", "--file", "/tmp/a b;rm"]);
});

test("launch uses tmux argv without shell", () => {
  const calls: [string, string[]][] = [];
  const runner: CommandRunner = { run(command, args) { calls.push([command, args]); return { status: calls.length === 1 ? 1 : 0 }; } };
  const run = launchInTmux(config(), { agent: "mock", repo: "/tmp/repo with space", prompt: "hello; touch /tmp/no", name: "safe" }, "run_1", runner);
  assert.equal(run.backend, "tmux");
  assert.equal(run.tmuxWindow, "safe");
  assert.equal(calls[1][0], "tmux"); assert.ok(calls[1][1].includes("--")); assert.ok(calls[1][1].includes("/tmp/repo with space"));
  assert.equal(calls[1][1].includes("sh"), false);
});

test("launch creates a Herdr workspace and starts a private argv wrapper", () => {
  const calls: [string, string[]][] = [];
  const runner: CommandRunner = { run(command, args) {
    calls.push([command, args]);
    if (args.includes("workspace") && args.includes("create")) return { status: 0, stdout: JSON.stringify({ result: { workspace: { workspace_id: "w2" }, tab: { tab_id: "w2:t1" }, root_pane: { pane_id: "w2:p1" } } }) };
    return { status: 0 };
  } };
  const c = config();
  const run = launchInHerdr(c, { agent: "mock", repo: "/tmp/repo with space", prompt: "hello; touch /tmp/no", name: "safe" }, "run_1", runner);
  assert.equal(run.backend, "herdr");
  assert.equal(run.herdrWorkspace, "w2");
  assert.deepEqual(calls[0], ["herdr", ["--session", "default", "workspace", "create", "--cwd", "/tmp/repo with space", "--label", "safe", "--no-focus"]]);
  assert.deepEqual(calls[1], ["herdr", ["--session", "default", "pane", "run", "w2:p1", "'" + join(c.stateDir, "runs", "run_1", "launch.sh") + "'"]]);
  assert.equal(calls[1][1].some(value => value.includes("hello; touch")), false);
});

test("launch can target an existing Herdr workspace with a new tab", () => {
  const calls: [string, string[]][] = [];
  const runner: CommandRunner = { run(command, args) {
    calls.push([command, args]);
    if (args.includes("tab") && args.includes("create")) return { status: 0, stdout: JSON.stringify({ result: { tab: { tab_id: "w9:t2" }, root_pane: { pane_id: "w9:p2" } } }) };
    return { status: 0 };
  } };
  const c = config();
  const run = launchInHerdr(c, { agent: "mock", repo: "/tmp", prompt: "hello", name: "reuse", workspaceId: "w9" }, "run_2", runner);
  assert.equal(run.herdrWorkspace, "w9");
  assert.equal(run.herdrTab, "w9:t2");
  assert.deepEqual(calls[0][1], ["--session", "default", "tab", "create", "--workspace", "w9", "--cwd", "/tmp", "--label", "reuse", "--no-focus"]);
});

test("API requires auth and exposes health/agents", async () => {
  const c = config(); writeFileSync(c.tokenFile, "secret", { mode: 0o600 });
  const server = createRuntimeServer(c, "secret"); await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address(); assert.ok(address && typeof address === "object"); const base = `http://127.0.0.1:${address.port}`;
  assert.equal((await fetch(base + "/health")).status, 401);
  assert.equal((await fetch(base + "/health", { headers: { authorization: "Bearer secret" } })).status, 200);
  assert.equal((await fetch(base + "/v1/agents")).status, 401);
  const response = await fetch(base + "/v1/agents", { headers: { authorization: "Bearer secret" } });
  assert.equal(response.status, 200); assert.deepEqual((await response.json() as any).agents[0].name, "mock");
  await new Promise<void>(resolve => server.close(() => resolve()));
});

test("non-loopback bind is rejected", () => assert.throws(() => createRuntimeServer({ ...config(), host: "0.0.0.0" as any }, "x"), /loopback/));
