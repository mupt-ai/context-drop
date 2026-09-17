import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { WorkerPool } from "../src/pool.js";
import { NativeWorkers } from "../src/native.js";
import { createRuntimeServer } from "../src/server.js";
import type { RuntimeConfig, Slot } from "../src/types.js";

class FakeNative extends NativeWorkers {
  launches: number[] = [];
  submissions: Array<[number, string]> = [];
  retired: string[] = [];
  override async launch(slot: Slot, _dir: string, persist: () => void) { slot.pane = `%${slot.id}`; slot.agent = this.config.workerAgent; this.launches.push(slot.id); persist(); }
  override async retire(slot: Slot) { this.retired.push(slot.pane!); }
  override async ready(_slot: Slot) {}
  override async reconcile(_slot: Slot) {}
  override async alive(_slot: Slot) { return true; }
  override async submit(slot: Slot, task: string) { this.submissions.push([slot.id, task]); }
}
function fixture() {
  const dir = mkdtempSync(join(tmpdir(), "context-drop-pool-"));
  const config: RuntimeConfig = { host: "127.0.0.1", port: 1, stateDir: dir, tokenFile: join(dir, "token"), workerAgent: "pi", reportCredentialsFile: join(dir, "managed", "report-credentials.json"), agents: { pi: { command: ["pi", "--approve"] } } };
  const native = new FakeNative(config);
  const pool = new WorkerPool(config, native);
  const source = join(dir, "main.jsonl");
  writeFileSync(source, [
    { type: "session", version: 3, id: "main", cwd: dir },
    { type: "message", id: "a", parentId: null, message: { role: "user", content: "old conversation" } },
    { type: "compaction", id: "b", parentId: "a", summary: "KEEP_COMPACTION", firstKeptEntryId: "a", tokensBefore: 123 },
    { type: "message", id: "c", parentId: "b", message: { role: "user", content: "new task" } },
    { type: "message", id: "d", parentId: "c", message: { role: "assistant", content: [{ type: "toolCall", name: "delegate_to_worker" }] } },
  ].map(value => JSON.stringify(value)).join("\n") + "\n");
  pool.setConversation({ path: source, leafId: "d" });
  const ready = async () => {
    await pool.start();
    for (const slot of pool.state.slots) pool.event(slot.capability, { worker: slot.id, id: `ready-${slot.id}`, type: "ready" });
  };
  return { pool, native, config, ready };
}
const settle = () => new Promise(resolve => setImmediate(resolve));
const owner = { routerId: "imessage-router", chatId: "chat" };

test("queued follow-ups preserve the session and publish each finished turn immediately", async t => {
  const { pool, native, config, ready } = fixture(); await ready();
  pool.setConversation({ ...pool.state.conversation!, instructions: "PARENT_AGENTS_STYLE" });
  const task = pool.enqueue({ ...owner, worker: 1, prompt: "A", requestId: "a" }); await settle();
  const session = task.session, turn = task.turnId, capability = task.capability;
  assert.equal(task.instructions, "PARENT_AGENTS_STYLE");
  assert.equal(pool.enqueue({ ...owner, worker: 1, prompt: "B", requestId: "b" }).id, task.id);
  pool.enqueue({ ...owner, worker: 1, prompt: "B", requestId: "b" });
  pool.enqueue({ ...owner, worker: 1, prompt: "C", requestId: "c" });
  assert.deepEqual(task.followups, ["B", "C"]);
  pool.close();
  const restored = new WorkerPool(config, native); t.after(() => restored.close()); await restored.start();
  const slot = restored.state.slots[0];
  restored.event(slot.capability, { worker: 1, id: "a-final", type: "final", runId: task.id, turnId: turn, message: "A result" });
  await settle();
  const continued = restored.state.tasks[0];
  assert.equal(continued.session, session);
  assert.equal(continued.prompt, "B\n\nC");
  assert.equal(continued.worker, 1);
  assert.equal(continued.status, "running");
  assert.equal(continued.capability, capability);
  const firstReport = restored.lease(owner.routerId, owner.chatId, 60)!;
  assert.equal(firstReport.message, "A result");
  assert.equal(firstReport.kind, "turn_completed");
  restored.finish(firstReport.id, { ...owner, leaseId: firstReport.leaseId }, true);
  // Duplicate or stale completion events must not publish the first answer twice.
  restored.event(slot.capability, { worker: 1, id: "a-final", type: "final", runId: task.id, turnId: turn, message: "A result" });
  restored.event(slot.capability, { worker: 1, id: "a-final-again", type: "final", runId: task.id, turnId: turn, message: "A result" });
  assert.equal(restored.state.reports.length, 1);
  assert.equal(native.submissions.length, 2);
  restored.event(slot.capability, { worker: 1, id: "abc-final", type: "final", runId: task.id, turnId: continued.turnId, message: "ABC result" });
  assert.deepEqual(restored.state.reports.map(r => r.message), ["A result", "ABC result"]);
  assert.deepEqual(restored.state.reports.map(r => r.kind), ["turn_completed", "completed"]);
  assert.equal(continued.status, "completed");
  assert.equal(continued.capability, "");
});

test("queued fragments merge, while explicitly separate work remains a new task", t => {
  const { pool } = fixture(); t.after(() => pool.close());
  const a = pool.enqueue({ ...owner, worker: 2, prompt: "A" });
  assert.equal(pool.enqueue({ ...owner, worker: 2, prompt: "B" }).id, a.id);
  assert.equal(a.prompt, "A\n\nB");
  assert.notEqual(pool.enqueue({ ...owner, worker: 2, prompt: "Unrelated", newTask: true }).id, a.id);
  assert.throws(() => pool.enqueue({ ...owner, chatId: "other", worker: 2, prompt: "injection" }), /another conversation/);
});

test("four warm native slots share one FIFO queue with schedules and preserve compaction", async t => {
  const { pool, native, ready } = fixture(); t.after(() => pool.close());
  await ready();
  const tasks = Array.from({ length: 5 }, (_, i) => pool.enqueue({ ...owner, prompt: `task ${i}`, ...(i === 4 ? { routerId: "scheduler" } : {}) }));
  await settle();
  assert.deepEqual(native.launches, [1, 2, 3, 4]);
  assert.equal(native.submissions.length, 4);
  assert.equal(tasks[4].status, "queued");
  const fork = readFileSync(tasks[0].session, "utf8");
  assert.match(fork, /KEEP_COMPACTION/);
  assert.doesNotMatch(fork, /delegate_to_worker/);
  const slot = pool.state.slots[0];
  pool.event(slot.capability, { worker: 1, id: "done-1", type: "final", runId: tasks[0].id, turnId: tasks[0].turnId, message: "finished" });
  await settle();
  assert.equal(tasks[4].worker, 1);
  assert.equal(native.launches.length, 4);
  assert.equal(native.submissions.length, 5);
  assert.equal(pool.state.reports[0].message, "finished");
});

test("questions pin their worker and an answer resumes the same fork", async t => {
  const { pool, native, ready } = fixture(); t.after(() => pool.close()); await ready();
  const task = pool.enqueue({ ...owner, worker: 2, prompt: "do work" }); await settle();
  const session = task.session, token = task.capability;
  pool.reportWithToken(token, { runId: task.id, message: "Which repository?", kind: "needs_user" });
  pool.event(pool.state.slots[1].capability, { worker: 2, id: "question-final", type: "final", runId: task.id, turnId: task.turnId, message: "Which repository?" });
  assert.equal(task.status, "waiting");
  assert.equal(pool.state.reports.length, 1);
  const answer = pool.enqueue({ ...owner, worker: 2, prompt: "the API repository" }); await settle();
  assert.equal(answer.id, task.id);
  assert.equal(answer.session, session);
  assert.equal(native.launches.length, 4);
  assert.equal(task.capability, token);
  assert.throws(() => pool.reportWithToken("different-task-token", { runId: task.id, message: "stale" }), /unauthorized/);
});

test("durable queue survives restart without replaying running tasks or creating panes", async t => {
  const { pool, native, config, ready } = fixture(); await ready();
  const first = pool.enqueue({ ...owner, worker: 1, prompt: "first", requestId: "occurrence" }); await settle();
  const queued = pool.enqueue({ ...owner, worker: 1, prompt: "queued", newTask: true }); await settle();
  assert.equal(pool.enqueue({ ...owner, prompt: "first", requestId: "occurrence" }).id, first.id);
  pool.close();
  const restored = new WorkerPool(config, native); t.after(() => restored.close()); await restored.start(); await settle();
  assert.equal(native.launches.length, 4);
  assert.equal(native.submissions.length, 1);
  assert.equal(restored.state.tasks.find(task => task.id === queued.id)?.status, "queued");
});

test("worker reports are scoped, final events deduplicate, and leases retry without duplicate completion", async t => {
  const { pool, ready } = fixture(); t.after(() => pool.close()); await ready();
  const task = pool.enqueue({ ...owner, prompt: "work" }); await settle();
  assert.throws(() => pool.reportWithToken("wrong", { runId: task.id, message: "spoof" }), /unauthorized/);
  const slot = pool.state.slots[0], event = { id: "final", worker: 1, type: "final", runId: task.id, turnId: task.turnId, message: "done" };
  pool.event(slot.capability, event); pool.event(slot.capability, event);
  assert.equal(pool.state.reports.length, 1);
  assert.equal(pool.lease(owner.routerId, "wrong-chat", 60), null);
  const report = pool.lease(owner.routerId, owner.chatId, 60)!;
  assert.equal(pool.lease(owner.routerId, owner.chatId, 60), null);
  pool.finish(report.id, { ...owner, leaseId: report.leaseId, errorClass: "ambiguous" }, false);
  assert.equal(pool.lease(owner.routerId, owner.chatId, 60), null);
});

test("main capability exposes only pool delegation; worker capability cannot control it", async t => {
  const { pool, config } = fixture();
  const server = createRuntimeServer(config, "daemon-secret", pool);
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise<void>(resolve => server.close(() => resolve())));
  const address = server.address() as { port: number };
  const base = `http://127.0.0.1:${address.port}`;
  const post = (path: string, token: string, value: unknown) => fetch(base + path, { method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" }, body: JSON.stringify(value) });
  const issued = await (await post("/v1/router-capabilities", "daemon-secret", owner)).json() as any;
  assert.equal((await post("/v1/workers/delegate", pool.state.slots[0].capability, { worker: 1, prompt: "bad" })).status, 401);
  assert.equal((await post("/v1/tasks/start", issued.capability, {})).status, 404);
  assert.equal((await post("/v1/workers/delegate", issued.capability, { worker: 5, prompt: "bad" })).status, 400);
  assert.equal((await post("/v1/workers/delegate", issued.capability, { worker: 1, prompt: "valid" })).status, 201);
});

test("a second runtime cannot open the same pool state", t => {
 const {pool, config, native} = fixture(); t.after(()=>pool.close());
 assert.throws(()=>new WorkerPool(config,native), /live runtime/);
});

test("resumed turn rejects old final events and deduplicates answer submissions", async t => {
 const {pool, ready, native} = fixture(); t.after(()=>pool.close()); await ready();
 const task=pool.enqueue({...owner,worker:1,prompt:"work",requestId:"request"}); await settle();
 const oldTurn=task.turnId;
 pool.reportWithToken(task.capability,{runId:task.id,message:"Which one?",kind:"needs_user"});
 pool.event(pool.state.slots[0].capability,{id:"old-final",worker:1,type:"final",runId:task.id,turnId:oldTurn,message:"Which one?"});
 pool.enqueue({...owner,worker:1,prompt:"the first",requestId:"answer"}); await settle();
 pool.event(pool.state.slots[0].capability,{id:"delayed-final",worker:1,type:"final",runId:task.id,turnId:oldTurn,message:"Which one?"});
 assert.equal(task.status,"running");
 assert.equal(pool.enqueue({...owner,worker:1,prompt:"the first",requestId:"answer"}).id, task.id);
 await settle(); assert.equal(native.submissions.length,2);
});

test("a fast native final during submission does not strand the queued task", async t => {
 const {pool,native,ready}=fixture(); t.after(()=>pool.close()); await ready();
 native.submit=async(slot, id)=>{
  const task=pool.state.tasks.find(task=>task.id===id)!;
  native.submissions.push([slot.id,id]);
  pool.event(slot.capability,{id:`final-${id}`,worker:slot.id,type:"final",runId:id,turnId:task.turnId,message:"done"});
  await settle();
 };
 pool.enqueue({...owner,worker:1,prompt:"first"});
 const second=pool.enqueue({...owner,worker:1,prompt:"second"});
 await settle(); await settle(); await settle();
 assert.equal(second.status,"completed"); assert.equal(native.submissions.length,2);
});

test("completed main tool loops remain in schedule forks", t => {
 const {pool,config}=fixture(); t.after(()=>pool.close());
 const source=pool.state.conversation!.path;
 const entries=readFileSync(source,"utf8").trim().split("\n").map(line=>JSON.parse(line));
 entries.at(-1).message.content[0].id="call";
 entries.push({type:"message",id:"e",parentId:"d",message:{role:"toolResult",toolCallId:"call",content:[{type:"text",text:"queued"}]}});
 entries.push({type:"message",id:"f",parentId:"e",message:{role:"assistant",content:[{type:"text",text:"FULL_MAIN_REPLY"}]}});
 writeFileSync(source,entries.map(value=>JSON.stringify(value)).join("\n")+"\n");
 pool.setConversation({path:source,leafId:"f"});
 const task=pool.enqueue({...owner,routerId:"scheduler",prompt:"scheduled task",repo:config.stateDir});
 assert.match(readFileSync(task.session,"utf8"),/FULL_MAIN_REPLY/);
});

test("a worker's final report completes its task and frees the slot once the pane settles", async t => {
 const {pool,native,ready}=fixture(); t.after(()=>pool.close()); await ready();
 const task=pool.enqueue({...owner,worker:1,prompt:"work"}); await settle();
 const report=pool.reportWithToken(task.capability,{runId:task.id,kind:"final",message:"Here is the answer."});
 assert.equal(report.kind,"completed"); assert.equal(report.message,"Here is the answer.");
 assert.equal(task.status,"completed"); assert.equal(task.capability,"");
 assert.throws(()=>pool.reportWithToken(task.capability,{runId:task.id,kind:"final",message:"again"}),/unauthorized/);
 // The pane's own idle detection arrives later and must not publish a second completion.
 pool.event(pool.state.slots[0].capability,{id:"late-idle",worker:1,type:"final",runId:task.id,turnId:task.turnId,message:""});
 assert.equal(pool.state.reports.length,1);
 const next=pool.enqueue({...owner,worker:1,prompt:"next"}); await settle();
 assert.equal(next.status,"running"); assert.equal(native.submissions.length,2);
 assert.throws(()=>pool.reportWithToken(next.capability,{runId:next.id,kind:"final",message:"   "}),/1–16000/);
});

test("changing the configured agent retires old panes on restart and relaunches", async t => {
 const {pool,native,config,ready}=fixture(); await ready();
 const task=pool.enqueue({...owner,worker:2,prompt:"work"}); await settle();
 assert.deepEqual(pool.state.slots.map(slot=>slot.agent),["pi","pi","pi","pi"]);
 pool.close();
 // A daemon restart builds a fresh runtime from the rewritten config.
 const switched:RuntimeConfig={...config,workerAgent:"claude",agents:{claude:{command:["claude"]}}};
 const relaunched=new FakeNative(switched);
 const restored=new WorkerPool(switched,relaunched); t.after(()=>restored.close()); await restored.start(); await settle();
 assert.deepEqual(relaunched.retired,["%1","%2","%3","%4"]);
 assert.deepEqual(relaunched.launches,[1,2,3,4]);
 assert.deepEqual(restored.state.slots.map(slot=>slot.agent),["claude","claude","claude","claude"]);
 assert.equal(restored.state.tasks.find(item=>item.id===task.id)?.status,"failed");
 assert.match(restored.state.reports[0].message,/agent changed to claude/);
 assert.equal(restored.workers()[0].agent,"claude");
});

test("readiness cannot dispatch before native registration completes", async t => {
 const {pool,native}=fixture();t.after(()=>pool.close());
 let release!:()=>void;
 const registration=new Promise<void>(resolve=>{release=resolve});
 native.launch=async(slot,_dir,persist)=>{
  slot.pane=`%${slot.id}`;persist();
  pool.event(slot.capability,{id:`early-ready-${slot.id}`,worker:slot.id,type:"ready"});
  await registration;
 };
 const task=pool.enqueue({...owner,worker:1,prompt:"work"});
 const starting=pool.start();await settle();
 assert.equal(native.submissions.length,0);
 assert.equal(pool.workers()[0].ready,false);
 release();await starting;await settle();
 assert.equal(task.status,"running");assert.equal(native.submissions.length,1);
});

test("workspace destination persists across followups and runtime restart", async t => {
  const { pool, native, config, ready } = fixture(); await ready();
  const workspaceTarget = { workspaceId: "project", workspaceLabel: "dari-mono", mode: "continue" as const, paneId: "agent", cwd: config.stateDir };
  const task = pool.enqueue({ ...owner, worker: 1, prompt: "continue costs", workspaceTarget }); await settle();
  assert.deepEqual(pool.workers()[0].workspaceTarget, workspaceTarget);
  pool.enqueue({ ...owner, worker: 1, prompt: "add tests" });
  assert.deepEqual(task.workspaceTarget, workspaceTarget);
  assert.throws(() => pool.enqueue({ ...owner, worker: 1, prompt: "other", workspaceTarget: { ...workspaceTarget, paneId: "other" } }), /different destination/);
  pool.close();
  const restored = new WorkerPool(config, native); t.after(() => restored.close());
  assert.deepEqual(restored.state.tasks[0].workspaceTarget, workspaceTarget);
});

test("workspace endpoints restrict capabilities and enqueue resolved destination", async t => {
  const { WorkspaceDirectory } = await import("../src/workspaces.js");
  const { pool, config } = fixture();
  const directory = new WorkspaceDirectory(config, async () => JSON.stringify({ result: {
    workspaces: [{ workspace_id: "project", label: "dari-mono" }],
    panes: [{ workspace_id: "project", pane_id: "user-agent", agent: "codex", cwd: config.stateDir }],
  } }));
  const server = createRuntimeServer(config, "secret", pool, directory);
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(() => server.close());
  const address = server.address() as { port: number };
  const call = (path: string, token: string, data?: object) => fetch(`http://127.0.0.1:${address.port}${path}`, { method: data ? "POST" : "GET", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" }, body: data ? JSON.stringify(data) : undefined });
  const { capability } = await (await call("/v1/router-capabilities", "secret", owner)).json() as { capability: string };
  assert.equal((await call("/v1/workspaces", pool.state.slots[0].capability)).status, 401);
  assert.equal((await call("/v1/workspaces", capability)).status, 200);
  const input = { worker: 1, workspace: "project", mode: "continue", paneId: "user-agent", prompt: "continue costs", requestId: "workspace-request" };
  assert.equal((await call("/v1/workspaces/delegate", pool.state.slots[0].capability, input)).status, 401);
  assert.equal((await call("/v1/workspaces/delegate", capability, { ...input, paneId: "foreign" })).status, 400);
  assert.equal(pool.state.tasks.length, 0);
  for (let i = 0; i < 2; i++) assert.equal((await call("/v1/workspaces/delegate", capability, input)).status, 201);
  assert.equal(pool.state.tasks.length, 1);
  assert.equal(pool.state.tasks[0].workspaceTarget?.paneId, "user-agent");
  assert.equal(pool.state.tasks[0].repo, config.stateDir);
});
