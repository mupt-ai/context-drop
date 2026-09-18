import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { chmodSync, existsSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { renderHistory } from "./history.js";
import { writePrivate } from "./state.js";
import type { RuntimeConfig, Slot, Task } from "./types.js";
import { workspaceInstructions } from "./workspaces.js";
import { WORKER_PROMPT } from "./prompts.js";

const exec = promisify(execFile);
export type Command = (command: string, args: string[]) => Promise<string>;
export const command: Command = async (program, args) => (await exec(program, args, { timeout: 30_000, maxBuffer: 1024 * 1024 })).stdout;
const quote = (text: string) => "'" + text.replaceAll("'", "'\\''") + "'";
// Each TUI's command for dropping the finished task's context before the next one.
const RESET_COMMAND: Record<string, string> = { claude: "/clear", codex: "/new", pi: "/new" };

interface Assignment { taskId: string; turnId: string; seqAtSubmit: number; submittedAt: number; sawWorking?: boolean; reported: boolean }
interface AgentState { agent?: string; status?: string; seq: number }

// Workers are interactive agents in Herdr tabs. Herdr's agent detection is the
// only signal for turn completion, so every worker is asked to post its final
// answer through `context-drop report --final` before its turn ends.
export class NativeWorkers {
  private assignments = new Map<number, Assignment>();
  private pollers = new Map<number, NodeJS.Timeout>();
  private stopped = false;
  onEvent?: (worker: number, event: any) => void;
  // Pane polling cadence; tests shrink it.
  timing = { registerMs: 30_000, settleMs: 15_000, pollMs: 100, watchMs: 2_000 };
  constructor(protected config: RuntimeConfig, private run: Command = command) {}
  private sleep(): Promise<void> { return new Promise(resolve => setTimeout(resolve, this.timing.pollMs)); }
  close(): void { this.stopped = true; for (const timer of this.pollers.values()) clearInterval(timer); this.pollers.clear(); }
  private assignmentPath(worker: number): string { return join(this.config.stateDir, "workers", String(worker), "assignment.json"); }
  private herdr(args: string[]): Promise<string> {
    return this.run(this.config.herdrPath || "herdr", ["--session", this.config.herdrSession || "default", ...args]);
  }
  private async agentState(pane: string): Promise<AgentState> {
    const agent = JSON.parse(await this.herdr(["agent", "get", pane])).result?.agent;
    return { agent: agent?.agent, status: agent?.agent_status, seq: Number(agent?.state_change_seq) || 0 };
  }

  // Claude Code asks whether to trust a workspace before it will accept a
  // prompt. The worker directory is Context Drop's own state, so record the
  // acceptance where Claude keeps it, keyed by the resolved path.
  private trustClaudeWorkspace(dir: string): void {
    const path = join(homedir(), ".claude.json");
    const state = existsSync(path) ? JSON.parse(readFileSync(path, "utf8")) : {};
    state.projects ??= {};
    const key = realpathSync(dir);
    state.projects[key] = { ...(state.projects[key] || {}), hasTrustDialogAccepted: true };
    writePrivate(path, state);
  }

  async launch(slot: Slot, dir: string, persist: () => void): Promise<void> {
    const [program, ...args] = this.config.agents[this.config.workerAgent]?.command || [];
    if (!program) throw new Error(`worker agent ${this.config.workerAgent} is not configured`);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    if (this.config.workerAgent === "claude") this.trustClaudeWorkspace(dir);
    const launcher = join(dir, "launch.sh");
    // Workers get none of the daemon's environment except where to find their
    // per-task report credentials, keyed by HERDR_PANE_ID, and the matching
    // context-drop binary ahead of whatever is installed on PATH.
    const unset = Object.keys(process.env).filter(key => key.startsWith("CONTEXT_DROP_") || key.startsWith("IMSG_") || key.startsWith("IMESSAGE_")).flatMap(key => ["-u", key]);
    const env = [...unset, `CONTEXT_DROP_REPORT_CREDENTIALS=${this.config.reportCredentialsFile}`];
    const path = this.config.contextDropPath ? `export PATH=${quote(dirname(this.config.contextDropPath))}:"$PATH"\n` : "";
    writeFileSync(launcher, `#!/bin/sh\n${path}exec env ${env.map(quote).join(" ")} ${[program, ...args].map(quote).join(" ")}\n`, { mode: 0o700 });
    chmodSync(launcher, 0o700);
    const label = this.config.fullAIHerdrWorkspaceLabel || "ContextDropManaged";
    const workspaces = JSON.parse(await this.herdr(["workspace", "list"])).result?.workspaces;
    if (!Array.isArray(workspaces)) throw new Error("Herdr returned invalid workspaces");
    const matches = workspaces.filter((item: any) => item.label === label);
    if (matches.length > 1) throw new Error("multiple Context Drop workspaces; configure a unique label");
    const result = JSON.parse(await this.herdr(matches.length
      ? ["tab", "create", "--workspace", matches[0].workspace_id, "--cwd", dir, "--label", `Worker ${slot.id}`, "--no-focus"]
      : ["workspace", "create", "--cwd", dir, "--label", label, "--no-focus"])).result;
    if (!result?.root_pane?.pane_id) throw new Error("Herdr did not return an exact pane ID");
    slot.pane = result.root_pane.pane_id;
    slot.agent = this.config.workerAgent;
    persist();
    await this.herdr(["pane", "run", slot.pane!, quote(launcher)]);
    await this.ready(slot);
  }

  async ready(slot: Slot): Promise<void> {
    const deadline = Date.now() + this.timing.registerMs;
    while (Date.now() < deadline) {
      try {
        if ((await this.agentState(slot.pane!)).agent === this.config.workerAgent) return;
      } catch { /* a new agent may not have registered yet */ }
      await this.sleep();
    }
    throw new Error(`native ${this.config.workerAgent} did not register in its pane`);
  }

  async alive(slot: Slot): Promise<boolean> {
    if (!slot.pane) return false;
    const panes = JSON.parse(await this.herdr(["pane", "list"])).result?.panes;
    if (!Array.isArray(panes)) throw new Error("Herdr returned invalid panes");
    return panes.some((pane: any) => pane.pane_id === slot.pane);
  }

  async retire(slot: Slot): Promise<void> {
    if (!slot.pane) return;
    try { await this.herdr(["pane", "close", slot.pane]); } catch { /* the pane may already be gone */ }
  }

  private async prompt(slot: Slot, text: string): Promise<void> {
    if (!slot.pane) throw new Error("worker has no native pane");
    await this.herdr(["agent", "prompt", slot.pane, text]);
  }
  private async waitIdle(pane: string): Promise<void> {
    const deadline = Date.now() + this.timing.settleMs;
    while (Date.now() < deadline) {
      const state = await this.agentState(pane);
      if (state.status === "idle" || state.status === "done") return;
      await this.sleep();
    }
  }
  private saveAssignment(worker: number, assignment: Assignment): void {
    this.assignments.set(worker, assignment);
    writeFileSync(this.assignmentPath(worker), JSON.stringify(assignment), { mode: 0o600 });
  }
  private grantReporting(pane: string, task: Task): void {
    const path = this.config.reportCredentialsFile;
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    const all = existsSync(path) ? JSON.parse(readFileSync(path, "utf8")) : {};
    all[pane] = { url: `http://${this.config.host === "::1" ? "[::1]" : this.config.host}:${this.config.port}/v1/reports`, capability: task.capability, runId: task.id };
    writePrivate(path, all);
  }

  async submit(slot: Slot, taskId: string, turnId: string): Promise<void> {
    const task: Task = JSON.parse(readFileSync(join(this.config.stateDir, "workers", String(slot.id), "task.json"), "utf8"));
    if (task.id !== taskId || task.turnId !== turnId) throw new Error("stale task assignment");
    const path = this.assignmentPath(slot.id);
    const previous: Assignment | undefined = this.assignments.get(slot.id) || (existsSync(path) ? JSON.parse(readFileSync(path, "utf8")) : undefined);
    if (previous?.turnId === turnId) { await this.reconcile(slot); return; }
    const contextPath = join(this.config.stateDir, "tasks", task.id, "context.md");
    const fresh = previous?.taskId !== task.id;
    // A final report can complete the task before the agent's turn ends; let
    // the pane settle so the next prompt is not typed into a working agent.
    await this.waitIdle(slot.pane!);
    if (fresh && previous) { await this.prompt(slot, RESET_COMMAND[this.config.workerAgent] || "/new"); await this.waitIdle(slot.pane!); }
    // Continuations receive current role instructions too: the briefing is
    // rewritten every turn but only announced on the first.
    writeFileSync(contextPath, [
      "# Context Drop worker briefing",
      task.instructions ? `## Parent instructions\n\n${task.instructions}` : "",
      `## Worker role\n\n${WORKER_PROMPT}`,
      task.workspaceTarget ? workspaceInstructions(task.workspaceTarget) : "",
      `## Parent conversation so far\n\n${renderHistory(task.session, join(this.config.stateDir, "tasks", task.id, "images"))}`,
    ].filter(Boolean).join("\n\n") + "\n", { mode: 0o600 });
    this.grantReporting(slot.pane!, task);
    const before = await this.agentState(slot.pane!);
    const assignment: Assignment = { taskId, turnId, seqAtSubmit: before.seq, submittedAt: Date.now(), reported: false };
    this.saveAssignment(slot.id, assignment);
    const text = fresh
      ? `Read ${contextPath} fully before acting; it contains your role, the parent conversation and how to report. Task:\n\n${task.prompt}`
      : task.workspaceTarget ? `Read ${contextPath} again for current role and destination instructions. Follow-up:\n\n${task.prompt}` : task.prompt;
    await this.prompt(slot, text);
    this.watch(slot);
  }

  private watch(slot: Slot): void {
    if (this.stopped || this.pollers.has(slot.id)) return;
    const timer = setInterval(() => { void this.reconcile(slot).catch(() => {}); }, this.timing.watchMs);
    timer.unref();
    this.pollers.set(slot.id, timer);
  }
  private finish(worker: number, assignment: Assignment, failed: boolean, message: string): void {
    if (assignment.reported) return;
    assignment.reported = true;
    this.saveAssignment(worker, assignment);
    const timer = this.pollers.get(worker);
    if (timer) { clearInterval(timer); this.pollers.delete(worker); }
    this.onEvent?.(worker, { id: `herdr:${assignment.taskId}:${assignment.turnId}`, worker, type: "final", runId: assignment.taskId, turnId: assignment.turnId, failed, message });
  }
  async reconcile(slot: Slot): Promise<void> {
    const path = this.assignmentPath(slot.id);
    if (!slot.pane || !existsSync(path)) return;
    const assignment: Assignment = this.assignments.get(slot.id) || JSON.parse(readFileSync(path, "utf8"));
    this.assignments.set(slot.id, assignment);
    if (assignment.reported) return;
    const state = await this.agentState(slot.pane);
    if (state.status === "working") { assignment.sawWorking = true; this.watch(slot); return; }
    if (state.status === "blocked") {
      this.finish(slot.id, assignment, true, `Worker is blocked waiting for input in its pane:\n${await this.screen(slot.pane)}`);
      return;
    }
    // Herdr's status does not track turns: an idle read right after submission
    // is the previous state unless the sequence advanced or work was observed.
    const settled = state.seq > assignment.seqAtSubmit || assignment.sawWorking || Date.now() - assignment.submittedAt > 3_000;
    if ((state.status === "idle" || state.status === "done") && settled) {
      // The worker's own final report normally lands first; when it did not,
      // the pane is the only record of what happened.
      this.finish(slot.id, assignment, false, `Worker ended its turn without a final report. Its pane shows:\n${await this.screen(slot.pane)}`);
    } else this.watch(slot);
  }
  private async screen(pane: string): Promise<string> {
    const text = await this.herdr(["agent", "read", pane, "--lines", "40"]);
    return text.split("\n").filter(line => line.trim()).join("\n").slice(-6000);
  }
}
