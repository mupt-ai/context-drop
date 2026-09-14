import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { CodexClient } from "../src/codex.js";
import { NativeWorkers } from "../src/native.js";
import { WORKER_PROMPT } from "../src/prompts.js";
import type { RuntimeConfig, Task } from "../src/types.js";

for (const continuation of [false, true]) {
  test(`worker ${continuation ? "continuation refreshes" : "fork receives"} execution authority before starting a turn`, async t => {
    const dir = mkdtempSync(join(tmpdir(), "context-drop-authority-"));
    t.after(() => rmSync(dir, { recursive: true, force: true }));
    const config: RuntimeConfig = { host: "127.0.0.1", port: 1, stateDir: dir, tokenFile: "unused", agents: {} };
    const task: Task = { id: "task", turnId: "turn", prompt: "save the reported meal", name: "health", repo: dir, session: join(dir, "session.jsonl"), routerId: "main", chatId: "chat", status: "running", capability: "report-token", createdAt: "now", requestIds: [], instructions: "CURRENT_PARENT_STYLE\nMain orchestrator: delegate health reads/writes." };
    mkdirSync(join(dir, "workers", "1"), { recursive: true });
    mkdirSync(join(dir, "tasks", task.id), { recursive: true });
    writeFileSync(task.session, JSON.stringify({ type: "session", version: 3, cwd: dir }) + "\n");
    writeFileSync(join(dir, "workers", "1", "task.json"), JSON.stringify(task));
    if (continuation) writeFileSync(join(dir, "tasks", task.id, "codex.json"), JSON.stringify({ threadId: "existing-thread" }));
    const calls: Array<{ method: string; params: any }> = [];
    t.mock.method(CodexClient.prototype, "start", async () => {});
    t.mock.method(CodexClient.prototype, "call", async (method: string, params: any) => {
      calls.push({ method, params });
      if (method === "turn/start") return { turn: { id: "codex-turn" } };
      return { thread: { id: method === "thread/fork" ? "fork-thread" : "parent", turns: [] } };
    });
    const native = new NativeWorkers(config, async () => "");
    await native.submit({ id: 1, backend: "tmux", pane: "%1", capability: "slot-token" }, task.id, task.turnId);
    const resume = calls.find(call => call.method === "thread/resume")!;
    assert.equal(resume.params.threadId, continuation ? "existing-thread" : "fork-thread");
    assert.equal(resume.params.approvalPolicy, "never");
    assert.equal(resume.params.sandbox, "danger-full-access");
    assert.equal(resume.params.developerInstructions, task.instructions + "\n\n" + WORKER_PROMPT);
    assert.match(resume.params.developerInstructions, /including Health API reads and writes/);
    assert.match(resume.params.developerInstructions, /Use Herdr for coding work/);
    assert.ok(calls.indexOf(resume) < calls.findIndex(call => call.method === "turn/start"));
    const fork = calls.find(call => call.method === "thread/fork");
    if (continuation) assert.equal(fork, undefined, "continuation must preserve the existing thread");
    else assert.equal(fork!.params.developerInstructions, resume.params.developerInstructions);
  });
}
