import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { NativeWorkers } from "../src/native.js";
import { WORKER_PROMPT } from "../src/prompts.js";
import type { RuntimeConfig, Slot, Task, WorkerAgent } from "../src/types.js";

// Scripts Herdr's CLI: every call is recorded and answered from a tiny agent model.
class FakeHerdr {
  calls: string[][] = [];
  agent = "claude";
  status = "idle";
  seq = 1;
  // Herdr may not have observed the new turn yet when the prompt returns.
  lazyPrompt = false;
  screen = "$ waiting for approval [y/N]";
  run = async (program: string, args: string[]): Promise<string> => {
    assert.equal(program, "herdr");
    assert.deepEqual(args.slice(0, 2), ["--session", "default"]);
    const call = args.slice(2);
    this.calls.push(call);
    switch (call.slice(0, 2).join(" ")) {
      case "agent get": return JSON.stringify({ result: { agent: { agent: this.agent, agent_status: this.status, state_change_seq: this.seq, pane_id: call[2] } } });
      case "agent prompt": if (!this.lazyPrompt) { this.status = "working"; this.seq++; } return "{}";
      case "agent read": return this.screen;
      case "workspace list": return JSON.stringify({ result: { workspaces: [{ workspace_id: "w1", label: "ContextDropManaged" }] } });
      case "tab create": return JSON.stringify({ result: { root_pane: { pane_id: `w1:p${this.calls.filter(c => c[0] === "tab").length}` } } });
      case "pane run": return "{}";
      case "pane list": return JSON.stringify({ result: { panes: [{ pane_id: "w1:p1" }] } });
      case "pane close": return "{}";
    }
    throw new Error(`unexpected herdr call: ${call.join(" ")}`);
  };
}

function fixture(t: any, agent: WorkerAgent = "claude") {
  const dir = mkdtempSync(join(tmpdir(), "context-drop-native-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const config: RuntimeConfig = {
    host: "127.0.0.1", port: 4242, stateDir: dir, tokenFile: "unused", workerAgent: agent, reportCredentialsFile: join(dir, "managed", "report-credentials.json"), contextDropPath: "/opt/context-drop/bin/context-drop",
    agents: { claude: { command: ["/opt/dari", "--claude", "--dangerously-skip-permissions"] }, codex: { command: ["/opt/dari", "--codex", "--yolo"] }, pi: { command: ["/opt/dari", "--pi", "--approve"] } },
  };
  const herdr = new FakeHerdr();
  herdr.agent = agent;
  const native = new NativeWorkers(config, herdr.run);
  native.timing = { registerMs: 50, settleMs: 50, pollMs: 5, watchMs: 20 };
  t.after(() => native.close());
  const events: any[] = [];
  native.onEvent = (_worker, event) => events.push(event);
  const slot: Slot = { id: 1, capability: "slot-token", pane: "w1:p1", agent };
  mkdirSync(join(dir, "workers", "1"), { recursive: true });
  const assign = (id: string, turnId: string, prompt: string): Task => {
    const task: Task = { id, turnId, prompt, name: "health", repo: dir, session: join(dir, "tasks", id, "session.jsonl"), routerId: "main", chatId: "chat", status: "running", capability: `cap-${id}`, createdAt: "now", requestIds: [], instructions: "CURRENT_PARENT_STYLE" };
    mkdirSync(join(dir, "tasks", id), { recursive: true });
    writeFileSync(task.session, [
      { type: "session", version: 3, cwd: dir },
      { type: "message", id: "a", message: { role: "user", content: "PARENT_ASKED" } },
      { type: "compaction", id: "b", summary: "OLD_SUMMARY", firstKeptEntryId: "a" },
      { type: "message", id: "c", message: { role: "assistant", content: [{ type: "text", text: "PARENT_REPLIED" }] } },
      { type: "message", id: "d", message: { role: "user", content: [{ type: "image", mimeType: "image/png", data: Buffer.from("PNGDATA").toString("base64") }, { type: "text", text: "what is this?" }] } },
    ].map(v => JSON.stringify(v)).join("\n") + "\n");
    writeFileSync(join(dir, "workers", "1", "task.json"), JSON.stringify(task));
    return task;
  };
  return { dir, config, herdr, native, events, slot, assign };
}

for (const agent of ["claude", "codex", "pi"] as const) {
  test(`${agent} workers launch through their configured interactive command`, async t => {
    const { dir, herdr, native, config } = fixture(t, agent);
    const slot: Slot = { id: 2, capability: "slot-token" };
    let persisted = false;
    await native.launch(slot, join(dir, "workers", "2"), () => { persisted = true; });
    assert.ok(persisted);
    assert.equal(slot.pane, "w1:p1");
    assert.equal(slot.agent, agent);
    const launcher = readFileSync(join(dir, "workers", "2", "launch.sh"), "utf8");
    assert.match(launcher, new RegExp(`'/opt/dari' '--${agent}' '${config.agents[agent].command[2]}'`));
    assert.doesNotMatch(launcher, /app-server/);
    assert.match(launcher, new RegExp(`'CONTEXT_DROP_REPORT_CREDENTIALS=${config.reportCredentialsFile}'`));
    assert.match(launcher, /export PATH='\/opt\/context-drop\/bin':"\$PATH"/);
    assert.deepEqual(herdr.calls.find(call => call[0] === "tab"), ["tab", "create", "--workspace", "w1", "--cwd", join(dir, "workers", "2"), "--label", "Worker 2", "--no-focus"]);
    assert.ok(herdr.calls.some(call => call[0] === "pane" && call[1] === "run" && call[2] === "w1:p1"));
  });
}

test("launch fails when Herdr registers a different agent kind", async t => {
  const { dir, herdr, native } = fixture(t, "codex");
  herdr.agent = "claude";
  await assert.rejects(native.launch({ id: 3, capability: "x" }, join(dir, "workers", "3"), () => {}), /native codex did not register/);
});

test("submit briefs the worker, grants reporting, and completes when the pane settles", async t => {
  const { dir, config, herdr, native, events, slot, assign } = fixture(t);
  const task = assign("task-a", "turn-1", "save the reported meal");
  await native.submit(slot, task.id, task.turnId);
  const prompt = herdr.calls.find(call => call[0] === "agent" && call[1] === "prompt")!;
  assert.equal(prompt[2], "w1:p1");
  assert.match(prompt[3], /^Read .*context\.md fully before acting/);
  assert.match(prompt[3], /Task:\n\nsave the reported meal$/);
  const briefing = readFileSync(join(dir, "tasks", task.id, "context.md"), "utf8");
  assert.match(briefing, /CURRENT_PARENT_STYLE/);
  assert.ok(briefing.includes(WORKER_PROMPT));
  assert.match(briefing, /OLD_SUMMARY[\s\S]*PARENT_ASKED[\s\S]*PARENT_REPLIED/);
  const image = join(dir, "tasks", task.id, "images", "image-1.png");
  assert.match(briefing, new RegExp(`\\[image: ${image}\\]\nwhat is this\\?`));
  assert.equal(readFileSync(image, "utf8"), "PNGDATA");
  assert.match(briefing, /context-drop report --final/);
  const credentials = JSON.parse(readFileSync(config.reportCredentialsFile, "utf8"));
  assert.deepEqual(credentials["w1:p1"], { url: "http://127.0.0.1:4242/v1/reports", capability: "cap-task-a", runId: "task-a" });
  assert.equal(events.length, 0);
  // Working agents are never final; an idle observed after work is.
  await native.reconcile(slot);
  assert.equal(events.length, 0);
  herdr.status = "idle";
  await native.reconcile(slot);
  assert.equal(events.length, 1);
  assert.deepEqual({ ...events[0], message: undefined }, { id: "herdr:task-a:turn-1", worker: 1, type: "final", runId: "task-a", turnId: "turn-1", failed: false, message: undefined });
  assert.match(events[0].message, /without a final report[\s\S]*waiting for approval/);
  await native.reconcile(slot);
  assert.equal(events.length, 1, "completion is reported once");
  assert.equal(JSON.parse(readFileSync(join(dir, "workers", "1", "assignment.json"), "utf8")).reported, true);
});

test("a stale idle read immediately after submission is not completion", async t => {
  const { herdr, native, events, slot, assign } = fixture(t);
  herdr.lazyPrompt = true;
  const task = assign("task-b", "turn-1", "quick");
  await native.submit(slot, task.id, task.turnId);
  await native.reconcile(slot);
  assert.equal(events.length, 0);
  herdr.seq++;
  await native.reconcile(slot);
  assert.equal(events.length, 1);
});

test("a new task resets the agent while a follow-up turn continues in place", async t => {
  const { herdr, native, slot, assign, events } = fixture(t, "pi");
  const first = assign("task-a", "turn-1", "first");
  await native.submit(slot, first.id, first.turnId);
  herdr.status = "idle";
  await native.reconcile(slot);
  assert.equal(events.length, 1);
  const prompts = () => herdr.calls.filter(call => call[0] === "agent" && call[1] === "prompt").map(call => call[3]);
  assert.equal(prompts().length, 1);
  const followup = assign("task-a", "turn-2", "and then this");
  herdr.status = "idle";
  await native.submit(slot, followup.id, followup.turnId);
  assert.deepEqual(prompts().slice(1), ["and then this"]);
  herdr.status = "idle";
  await native.reconcile(slot);
  const next = assign("task-b", "turn-1", "fresh work");
  herdr.status = "idle";
  await native.submit(slot, next.id, next.turnId);
  assert.equal(prompts()[2], "/new");
  assert.match(prompts()[3], /fresh work$/);
  // Repeating the same turn only reconciles; it never re-prompts.
  await native.submit(slot, next.id, next.turnId);
  assert.equal(prompts().length, 4);
});

test("a blocked pane fails the turn with its screen so the main can intervene", async t => {
  const { herdr, native, events, slot, assign } = fixture(t, "codex");
  const task = assign("task-c", "turn-1", "install things");
  await native.submit(slot, task.id, task.turnId);
  herdr.status = "blocked";
  await native.reconcile(slot);
  assert.equal(events.length, 1);
  assert.equal(events[0].failed, true);
  assert.match(events[0].message, /blocked waiting for input[\s\S]*waiting for approval/);
});

test("retire closes the pane and alive reflects Herdr's pane list", async t => {
  const { dir, herdr, native, slot } = fixture(t);
  assert.equal(await native.alive(slot), true);
  assert.equal(await native.alive({ id: 4, capability: "x", pane: "w1:p9" }), false);
  await native.retire(slot);
  assert.deepEqual(herdr.calls.at(-1), ["pane", "close", "w1:p1"]);
  assert.equal(existsSync(join(dir, "managed")), false, "retiring a pane grants nothing");
});

test("workspace routing is in both fresh briefing and continuation without touching user panes", async t => {
  const { dir, native, herdr, slot, assign } = fixture(t);
  const task = assign("workspace-task", "turn-1", "continue costs");
  task.workspaceTarget = { workspaceId: "project", workspaceLabel: "dari-mono", mode: "continue", paneId: "user-agent", cwd: dir };
  writeFileSync(join(dir, "workers", "1", "task.json"), JSON.stringify(task));
  await native.submit(slot, task.id, task.turnId);
  const briefing = readFileSync(join(dir, "tasks", task.id, "context.md"), "utf8");
  assert.match(briefing, /user-agent/);
  assert.match(briefing, /NEVER send \/new or \/clear/);
  herdr.status = "idle";
  task.turnId = "turn-2"; task.prompt = "also tests";
  writeFileSync(join(dir, "workers", "1", "task.json"), JSON.stringify(task));
  await native.submit(slot, task.id, task.turnId);
  const prompts = herdr.calls.filter(c => c[0] === "agent" && c[1] === "prompt");
  assert.ok(prompts.every(c => c[2] === slot.pane));
  assert.match(prompts.at(-1)![3], /destination instructions/);
});
