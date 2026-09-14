import { randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, statSync, unlinkSync, writeFileSync } from "node:fs";
import { isAbsolute, join } from "node:path";
import { NativeWorkers } from "./native.js";
import { writePrivate } from "./state.js";
import type { Conversation, ParentReport, PoolState, RuntimeConfig, Task } from "./types.js";

export const POOL_SIZE = 4;
const secret = () => randomBytes(32).toString("base64url");
export function matches(a: string, b: string): boolean {
  const left = Buffer.from(a), right = Buffer.from(b);
  return left.length > 0 && left.length === right.length && timingSafeEqual(left, right);
}
export { writePrivate };

export class WorkerPool {
  readonly state: PoolState;
  private path: string;
  private lockPath: string;
  private lockToken = randomUUID();
  private ready = new Set<number>();
  private dispatching = false;
  private dispatchAgain = false;
  private stopped = false;
  private timer?: NodeJS.Timeout;
  private startup?: Promise<void>;

  constructor(readonly config: RuntimeConfig, private native = new NativeWorkers(config)) {
    mkdirSync(config.stateDir, { recursive: true, mode: 0o700 });
    this.lockPath = join(config.stateDir, "worker-pool.lock");
    if (existsSync(this.lockPath)) {
      const existing = JSON.parse(readFileSync(this.lockPath, "utf8"));
      if (!Number.isInteger(existing.pid) || existing.pid <= 0) throw new Error("invalid pool writer lock");
      try { process.kill(existing.pid, 0); throw new Error("worker pool already has a live runtime"); }
      catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
        unlinkSync(this.lockPath);
      }
    }
    const lock = openSync(this.lockPath, "wx", 0o600);
    try { writeFileSync(lock, JSON.stringify({ pid: process.pid, token: this.lockToken })); } finally { closeSync(lock); }
    this.path = join(config.stateDir, "worker-pool.json");
    this.state = existsSync(this.path) ? JSON.parse(readFileSync(this.path, "utf8")) : {
      slots: Array.from({ length: POOL_SIZE }, (_, i) => ({ id: i + 1, capability: secret() })), tasks: [], reports: [], events: [],
    };
    if (this.state.slots.length !== POOL_SIZE) throw new Error("pool state must contain exactly four worker slots");
    this.native.onEvent = (worker, event) => this.event(this.state.slots[worker - 1].capability, event);
    this.save();
  }
  private save(): void { writePrivate(this.path, this.state); }
  private dir(id: number): string { return join(this.config.stateDir, "workers", String(id)); }
  private current(id: number): Task | undefined { return this.state.tasks.find(task => task.worker === id && (task.status === "running" || task.status === "waiting")); }
  workers() {
    return this.state.slots.map(slot => ({ worker: slot.id, paneId: slot.pane, backend: "herdr", agent: slot.agent || this.config.workerAgent, ready: this.ready.has(slot.id) && !slot.launching, task: this.current(slot.id)?.id, prompt: this.current(slot.id)?.prompt.slice(0, 500), question: this.current(slot.id)?.question, status: this.current(slot.id)?.status || (this.ready.has(slot.id) && !slot.launching ? "idle" : "starting"), error: slot.error, queued: this.state.tasks.filter(task => task.status === "queued" && task.requestedWorker === slot.id).length }));
  }
  setConversation(value: Conversation): void {
    if (!isAbsolute(value.path) || !statSync(value.path).isFile() || (value.leafId !== null && typeof value.leafId !== "string")) throw new Error("invalid main conversation");
    if (value.instructions !== undefined && (typeof value.instructions !== "string" || value.instructions.length > 256000)) throw new Error("invalid parent instructions");
    this.state.conversation = value;
    this.save();
  }
  start(): Promise<void> {
    return this.startup ??= this.initialize();
  }
  private async initialize(): Promise<void> {
    // Serialize native layout creation so four launches cannot create competing workspaces.
    for (const slot of this.state.slots) {
      if (this.stopped) return;
      const dir = this.dir(slot.id);
      mkdirSync(dir, { recursive: true, mode: 0o700 });
      writePrivate(join(dir, "worker.json"), { worker: slot.id, capability: slot.capability, url: `http://${this.config.host === "::1" ? "[::1]" : this.config.host}:${this.config.port}` });
      if (slot.pane && slot.agent !== this.config.workerAgent) {
        // The configured agent changed since this pane was launched, or the
        // pane predates agent tracking; either way replace it.
        const task = this.current(slot.id);
        if (task) this.complete(task, `Worker agent changed to ${this.config.workerAgent}. Task was not replayed.`, true);
        await this.native.retire(slot);
        delete slot.pane;
        delete slot.agent;
        slot.launching = false;
      }
      if (slot.pane) {
        if (await this.native.alive(slot)) { await this.native.ready(slot); await this.native.reconcile(slot); slot.launching = false; this.ready.add(slot.id); this.save(); continue; }
        const task = this.current(slot.id);
        if (task) this.complete(task, "Worker pane disappeared. Task was not replayed.", true);
        delete slot.pane;
        slot.launching = false;
      }
      if (slot.launching) continue; // An interrupted native launch must never create a duplicate pane.
      writeFileSync(join(dir, "idle.jsonl"), JSON.stringify({ type: "session", version: 3, id: randomUUID(), timestamp: new Date().toISOString(), cwd: dir }) + "\n", { mode: 0o600 });
      slot.launching = true;
      this.save();
      try {
        await this.native.launch(slot, dir, () => this.save());
        slot.launching = false;
        this.ready.add(slot.id);
        delete slot.error;
      } catch (error) {
        slot.error = error instanceof Error ? error.message : "native launch failed";
      } finally { this.save(); }
    }
    this.timer = setInterval(() => { void this.monitor().catch(error => console.error("worker pool monitor:", error.message)); }, 10_000);
    this.timer.unref();
    void this.dispatch();
  }
  private async monitor(): Promise<void> {
    for (const slot of this.state.slots) {
      if (slot.launching || !slot.pane) continue;
      if (await this.native.alive(slot)) { await this.native.reconcile(slot); continue; }
      this.ready.delete(slot.id);
      const task = this.current(slot.id);
      if (task) this.complete(task, "Worker pane disappeared. Task was not replayed.", true);
      delete slot.pane;
      slot.launching = true;
      this.save();
      try {
        await this.native.launch(slot, this.dir(slot.id), () => this.save());
        slot.launching = false;
        this.ready.add(slot.id);
        delete slot.error;
      } catch (error) { slot.error = error instanceof Error ? error.message : "native replacement failed"; }
      finally { this.save(); }
    }
    void this.dispatch();
  }
  close(): void {
    this.stopped = true;
    this.native.close();
    if (this.timer) clearInterval(this.timer);
    if (existsSync(this.lockPath) && JSON.parse(readFileSync(this.lockPath, "utf8")).token === this.lockToken) unlinkSync(this.lockPath);
    // Native agents belong to the durable pool and survive runtime restarts.
  }

  private snapshot(id: string, repo?: string): { session: string; repo: string; instructions?: string } {
    const source = this.state.conversation;
    if (!source) throw new Error("main conversation is not ready; no task was queued");
    const entries = readFileSync(source.path, "utf8").trim().split("\n").map(line => JSON.parse(line));
    const header = entries.find(entry => entry.type === "session");
    if (!header || header.version !== 3) throw new Error("unsupported Pi session format");
    const byID = new Map(entries.filter(entry => entry.id && entry.type !== "session").map(entry => [entry.id, entry]));
    const branch: any[] = [];
    let cursor = source.leafId;
    const seen = new Set<string>();
    while (cursor) {
      if (seen.has(cursor)) throw new Error("conversation contains a cycle");
      seen.add(cursor);
      const entry = byID.get(cursor);
      if (!entry) throw new Error("main conversation snapshot is incomplete");
      branch.push(entry);
      cursor = entry.parentId;
    }
    branch.reverse();
    // A delegation tool call is still in flight. Exclude that unfinished main
    // tool loop rather than giving the worker orphaned tool calls.
    const inFlight = branch.findLastIndex(entry => entry.type === "message" && entry.message?.role === "assistant" && entry.message?.content?.some?.((block: any) => block.type === "toolCall" && block.name === "delegate_to_worker"));
    if (inFlight >= 0) {
      const calls = branch[inFlight].message.content.filter((block: any) => block.type === "toolCall");
      const results = new Set(branch.slice(inFlight + 1).filter(entry => entry.message?.role === "toolResult").map(entry => entry.message.toolCallId));
      if (calls.some((call: any) => !call.id || !results.has(call.id))) branch.splice(inFlight);
    }
    const cwd = repo || header.cwd;
    if (!isAbsolute(cwd) || !statSync(cwd).isDirectory()) throw new Error("task repository must be an existing absolute directory");
    const dir = join(this.config.stateDir, "tasks", id);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    const session = join(dir, "session.jsonl");
    writeFileSync(session, [JSON.stringify({ ...header, id: randomUUID(), cwd, timestamp: new Date().toISOString(), parentSession: source.path }), ...branch.map(entry => JSON.stringify(entry))].join("\n") + "\n", { mode: 0o600 });
    return { session, repo: cwd, instructions: source.instructions };
  }
  enqueue(input: { worker?: number; prompt: string; name?: string; repo?: string; routerId: string; chatId: string; requestId?: string; newTask?: boolean }): Task {
    if (typeof input.prompt !== "string" || !input.prompt.trim() || input.prompt.length > 16000) throw new Error("prompt must contain 1–16000 characters");
    if (input.worker !== undefined && (!Number.isInteger(input.worker) || input.worker < 1 || input.worker > POOL_SIZE)) throw new Error("worker must be 1, 2, 3, or 4");
    const duplicate = input.requestId && this.state.tasks.find(task => task.requestIds.includes(input.requestId!) && task.routerId === input.routerId && task.chatId === input.chatId);
    if (duplicate) return duplicate;
    const current = input.worker === undefined || input.newTask ? undefined : this.current(input.worker) || this.state.tasks.find(task => task.requestedWorker === input.worker && task.status === "queued");
    if (current && current.chatId !== input.chatId) throw new Error("worker belongs to another conversation");
    if (current?.status === "running" || current?.status === "queued") {
      if (current.routerId !== input.routerId) throw new Error("worker is on another task; choose an idle worker or set newTask");
      const pending = current.status === "queued" ? [current.prompt] : current.followups || [];
      if ([...pending, input.prompt].join("\n\n").length > 64000) throw new Error("too much pending context for this task");
      if (current.status === "queued") current.prompt += "\n\n" + input.prompt;
      else current.followups = [...pending, input.prompt];
      current.instructions = this.state.conversation?.instructions ?? current.instructions;
      if (input.requestId) current.requestIds.push(input.requestId);
      this.save();
      return current;
    }
    if (current?.status === "waiting") {
      if (current.chatId !== input.chatId) throw new Error("worker belongs to another conversation");
      current.prompt = input.prompt;
      current.status = "queued";
      current.requestedWorker = input.worker;
      current.turnId = randomUUID();
      current.instructions = this.state.conversation?.instructions ?? current.instructions;
      if (input.requestId) current.requestIds.push(input.requestId);
      delete current.question;
      this.save();
      void this.dispatch();
      return current;
    }
    if (this.state.tasks.filter(task => task.status === "queued").length >= 256) throw new Error("worker queue is full");
    const id = randomUUID();
    const task: Task = { id, ...this.snapshot(id, input.repo), prompt: input.prompt, name: input.name || "User task", requestedWorker: input.worker, routerId: input.routerId, chatId: input.chatId, requestIds: input.requestId ? [input.requestId] : [], turnId: randomUUID(), status: "queued", capability: secret(), createdAt: new Date().toISOString() };
    this.state.tasks.push(task);
    this.save();
    void this.dispatch();
    return task;
  }
  private async dispatch(): Promise<void> {
    if (this.stopped) return;
    if (this.dispatching) { this.dispatchAgain = true; return; }
    this.dispatching = true;
    try {
      for (const slot of this.state.slots) {
        if (slot.launching || !this.ready.has(slot.id) || this.current(slot.id)) continue;
        const task = this.state.tasks.find(task => task.status === "queued" && (!task.requestedWorker || task.requestedWorker === slot.id));
        if (!task) continue;
        task.worker = slot.id;
        task.status = "running";
        writePrivate(join(this.dir(slot.id), "task.json"), task);
        this.save();
        try { await this.native.submit(slot, task.id, task.turnId); }
        catch {
          // Input may have reached the native agent. Keep the slot occupied until
          // its final event or proven pane exit; never auto-replay ambiguous work.
          this.report(task, "progress", "Native worker dispatch could not be confirmed. This task will not be replayed automatically.");
        }
      }
    } finally {
      this.dispatching = false;
      if (this.dispatchAgain) { this.dispatchAgain = false; void this.dispatch(); }
    }
  }
  report(task: Task, kind: ParentReport["kind"], message: string): ParentReport {
    if (!message.trim() || message.length > 16000) throw new Error("report must contain 1–16000 characters");
    const report: ParentReport = { id: randomUUID(), runId: task.id, worker: task.worker!, routerId: task.routerId, chatId: task.chatId, kind, message, createdAt: new Date().toISOString() };
    if (kind === "needs_user") task.question = message;
    this.state.reports.push(report);
    this.save();
    return report;
  }
  private complete(task: Task, message: string, failed: boolean): void {
    if (!failed && task.followups?.length) {
      task.prompt = task.followups.join("\n\n");
      delete task.followups;
      delete task.question;
      task.status = "queued";
      task.requestedWorker = task.worker;
      task.turnId = randomUUID();
      // Publish this turn's answer without retiring the task or its capability.
      this.report(task, "turn_completed", message.trim() || "Worker finished without a final answer.");
      return;
    }
    if (!failed && task.question) task.status = "waiting";
    else {
      task.status = failed ? "failed" : "completed";
      task.capability = "";
      this.report(task, failed ? "failed" : "completed", message.trim() || (failed ? "Worker failed without a final answer." : "Worker finished without a final answer."));
    }
    this.save();
  }
  event(token: string, event: any): void {
    const slot = this.state.slots.find(slot => slot.id === event.worker && matches(token, slot.capability));
    if (!slot) throw new Error("unauthorized worker");
    if (typeof event.id !== "string" || !event.id) throw new Error("event ID is required");
    if (this.state.events.includes(event.id)) return;
    if (event.type === "ready") this.ready.add(slot.id);
    else if (event.type === "final") {
      const task = this.state.tasks.find(task => task.id === event.runId && task.worker === slot.id);
      if (!task) throw new Error("unknown worker task");
      if (task.status === "running" && event.turnId === task.turnId) this.complete(task, String(event.message || "").slice(0, 16000), event.failed === true);
    } else throw new Error("unknown worker event");
    this.state.events = [...this.state.events.slice(-1999), event.id];
    this.save();
    void this.dispatch();
  }
  reportWithToken(token: string, input: any): ParentReport {
    const task = this.state.tasks.find(task => task.id === input.runId && task.status === "running" && matches(token, task.capability));
    if (!task) throw new Error("unauthorized report");
    if (input.kind === "final") {
      // The worker's own answer beats the eventual idle detection, which only
      // knows that the turn ended. The slot stays busy until that happens.
      const message = String(input.message || "");
      if (!message.trim() || message.length > 16000) throw new Error("report must contain 1–16000 characters");
      this.complete(task, message, false);
      return this.state.reports.findLast(report => report.runId === task.id)!;
    }
    if (input.kind !== undefined && input.kind !== "progress" && input.kind !== "needs_user") throw new Error("only progress, question, and final reports are accepted");
    return this.report(task, input.kind || "progress", String(input.message || ""));
  }
  lease(routerId: string, chatId: string, seconds: number): ParentReport | null {
    const now = Date.now();
    const report = this.state.reports.find(report => report.routerId === routerId && report.chatId === chatId && !report.deliveredAt && (report.leaseUntil || 0) <= now && (report.nextAttemptAt || 0) <= now);
    if (!report) return null;
    report.leaseId = randomUUID();
    report.leaseUntil = now + Math.min(3600, Math.max(60, seconds || 1320)) * 1000;
    this.save();
    return report;
  }
  finish(id: string, input: any, ack: boolean): void {
    const report = this.state.reports.find(report => report.id === id && report.routerId === input.routerId && report.chatId === input.chatId && report.leaseId === input.leaseId);
    if (!report) throw new Error("report lease does not match");
    if (ack) report.deliveredAt = new Date().toISOString();
    else {
      report.attempts = (report.attempts || 0) + 1;
      report.nextAttemptAt = ["ambiguous", "permanent"].includes(input.errorClass) ? Number.MAX_SAFE_INTEGER : Date.now() + Math.min(300_000, 1000 * 2 ** Math.min(8, report.attempts));
    }
    delete report.leaseId;
    delete report.leaseUntil;
    this.save();
  }
}
