import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, readFileSync, statSync } from "node:fs";
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

test("delegate launches in private worker cwd and scoped reports persist idempotently", async () => {
  const calls: [string, string[]][] = [];
  const runner: CommandRunner = { run(command, args) {
    calls.push([command, args]);
    if (args.includes("workspace") && args.includes("create")) return { status: 0, stdout: JSON.stringify({ result: { workspace: { workspace_id: "worker-w" }, tab: { tab_id: "worker-t" }, root_pane: { pane_id: "worker-p" } } }) };
    return { status: 0 };
  } };
  const c = { ...config(), defaultBackend: "herdr" as const, delegateAgent: "mock" };
  const server = createRuntimeServer(c, "general-secret", runner); await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address(); assert.ok(address && typeof address === "object"); const base = `http://127.0.0.1:${address.port}`;
  const headers = { authorization: "Bearer general-secret", "content-type": "application/json" };
  const capResponse = await fetch(base + "/v1/delegate-capability", { headers }); const { capability } = await capResponse.json() as any;
  assert.equal(capResponse.status, 200); assert.equal(statSync(join(c.stateDir, "delegate-capability")).mode & 0o777, 0o600);
  assert.equal(readFileSync(join(c.stateDir, "delegate-capability"), "utf8").includes("general-secret"), false);
  const delegate = await fetch(base + "/v1/delegate", { method: "POST", headers: { authorization: `Bearer ${capability}`, "content-type": "application/json" }, body: JSON.stringify({ task: "book a tee time only after payment confirmation", chatID: "chat-1" }) });
  assert.equal(delegate.status, 201); const launched = await delegate.json() as any; assert.equal(launched.run.backend, "herdr");
  const cwd = join(c.stateDir, "..", "delegation", "workers"); assert.ok(calls[0][1].includes(cwd)); assert.equal(calls[0][1].includes("book a tee time only after payment confirmation"), false);
  const launchScript = readFileSync(join(c.stateDir, "runs", launched.run.id, "launch.sh"), "utf8");
  const capMatch = launchScript.match(/CONTEXT_DROP_REPORT_CAPABILITY='([^']+)'/); assert.ok(capMatch); assert.equal(launchScript.includes("general-secret"), false); assert.match(launchScript, /report_to_parent_extension\.js/);
  const unauthorized = await fetch(base + "/v1/reports", { method: "POST", headers: { authorization: "Bearer wrong", "content-type": "application/json" }, body: JSON.stringify({ runId: launched.run.id, kind: "completed", message: "done" }) }); assert.equal(unauthorized.status, 401);
  const reportResponse = await fetch(base + "/v1/reports", { method: "POST", headers: { authorization: `Bearer ${capMatch[1]}`, "content-type": "application/json" }, body: JSON.stringify({ runId: launched.run.id, kind: "completed", message: "tee time booked after confirmation" }) });
  assert.equal(reportResponse.status, 201); const { report } = await reportResponse.json() as any; assert.match(readFileSync(join(c.stateDir, "parent-reports.jsonl"), "utf8"), /tee time booked/);
  const pending = await fetch(base + "/v1/reports", { headers }); assert.equal((await pending.json() as any).reports.length, 1);
  const claim = await fetch(base + `/v1/reports/${report.id}/deliver`, { method: "POST", headers }); assert.equal(claim.status, 200);
  const duplicate = await fetch(base + `/v1/reports/${report.id}/deliver`, { method: "POST", headers }); assert.equal(duplicate.status, 409);
  const after = await fetch(base + "/v1/reports", { headers }); assert.equal((await after.json() as any).reports.length, 0);
  const delegations = await fetch(base + "/v1/delegations", { headers }); const tasks = (await delegations.json() as any).tasks; assert.equal(tasks[0].status, "completed"); assert.equal(tasks[0].chatID, "chat-1");
  await new Promise<void>(resolve => server.close(() => resolve()));
});

test("delegate failure is precise and does not persist a phantom task", async () => {
  const c = { ...config(), defaultBackend: "tmux" as const, delegateAgent: "missing" };
  const server = createRuntimeServer(c, "secret"); await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address(); assert.ok(address && typeof address === "object"); const base = `http://127.0.0.1:${address.port}`;
  const response = await fetch(base + "/v1/delegate", { method: "POST", headers: { authorization: "Bearer secret", "content-type": "application/json" }, body: JSON.stringify({ task: "work" }) });
  assert.equal(response.status, 400); assert.match((await response.json() as any).error, /unknown agent: missing/);
  assert.equal((await (await fetch(base + "/v1/delegations", { headers: { authorization: "Bearer secret" } })).json() as any).tasks.length, 0);
  await new Promise<void>(resolve => server.close(() => resolve()));
});

test("non-loopback bind is rejected", () => assert.throws(() => createRuntimeServer({ ...config(), host: "0.0.0.0" as any }, "x"), /loopback/));
