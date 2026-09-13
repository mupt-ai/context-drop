import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { chmodSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { CodexClient, codexHistory } from "./codex.js";
import type { RuntimeConfig, Slot, Task } from "./types.js";
import { WORKER_PROMPT } from "./prompts.js";

const exec = promisify(execFile);
export type Command = (command: string, args: string[]) => Promise<string>;
export const command: Command = async (program, args) => (await exec(program, args, { timeout: 30_000, maxBuffer: 1024 * 1024 })).stdout;
const quote = (text: string) => "'" + text.replaceAll("'", "'\\''") + "'";

export class NativeWorkers {
  private codex: CodexClient;
  private assignments = new Map<number, any>();
  onEvent?: (worker: number, event: any) => void;
  constructor(private config: RuntimeConfig, private run: Command = command) {
    this.codex = new CodexClient(config);
    this.codex.onNotification = (method, params) => {
      if (method === "turn/completed") {
        for (const [worker, assignment] of this.assignments) {
          if (assignment.threadId === params.threadId && (!assignment.codexTurnId || assignment.codexTurnId === params.turn.id)) this.finish(worker, assignment, params.turn);
        }
      }
    };
  }
  close(): void { this.codex.close(); }
  private assignmentPath(worker: number): string { return join(this.config.stateDir, "workers", String(worker), "codex-assignment.json"); }
  private finish(worker: number, assignment: any, turn: any): void {
    if (assignment.reported) return;
    const final = turn.items?.findLast((item: any) => item.type === "agentMessage" && item.phase !== "commentary");
    const failed = turn.status !== "completed";
    this.onEvent?.(worker, { id: `codex:${assignment.threadId}:${turn.id}`, worker, type: "final", runId: assignment.taskId, turnId: assignment.turnId, failed, message: final?.text || turn.error?.message || (failed ? "Codex turn failed." : "") });
    assignment.reported = true;
    writeFileSync(this.assignmentPath(worker), JSON.stringify(assignment), { mode: 0o600 });
  }
  async reconcile(slot: Slot): Promise<void> {
    await this.codex.start();
    const path = this.assignmentPath(slot.id);
    if (!existsSync(path)) return;
    const assignment = this.assignments.get(slot.id) || JSON.parse(readFileSync(path, "utf8"));
    this.assignments.set(slot.id, assignment);
    if (assignment.reported) return;
    await this.codex.call("thread/resume", { threadId: assignment.threadId, excludeTurns: true });
    const { thread } = await this.codex.call("thread/read", { threadId: assignment.threadId, includeTurns: true });
    const candidates = thread.turns?.filter((turn: any) => !assignment.previousTurnIds?.includes(turn.id)) || [];
    const turn = assignment.codexTurnId ? thread.turns?.find((turn: any) => turn.id === assignment.codexTurnId) : candidates.length === 1 ? candidates[0] : undefined;
    if (turn && turn.status !== "inProgress") this.finish(slot.id, assignment, turn);
  }
  private herdr(args: string[]): Promise<string> {
    return this.run(this.config.herdrPath || "herdr", ["--session", this.config.herdrSession || "default", ...args]);
  }

  async launch(slot: Slot, dir: string, persist: () => void): Promise<void> {
    const [codex, ...args] = this.config.agents.codex?.command || [];
    if (!codex) throw new Error("four-worker pool requires Codex");
    await this.codex.start();
    if (!slot.threadId) {
      const { thread } = await this.codex.call("thread/start", { cwd: dir, approvalPolicy: "never", sandbox: "danger-full-access", developerInstructions: WORKER_PROMPT, historyMode: "legacy" });
      slot.threadId = thread.id;
      await this.codex.call("thread/name/set", { threadId: thread.id, name: `Context Drop worker ${slot.id}` });
      persist();
    }
    // Permission policy belongs to the remote server; Codex rejects it on resume.
    const tuiArgs = args.filter(arg => !["--yolo", "--dangerously-bypass-approvals-and-sandbox"].includes(arg));
    const argv = [codex, ...tuiArgs, "resume", slot.threadId!, "--remote", this.codex.address, "--remote-auth-token-env", "CONTEXT_DROP_CODEX_TOKEN", "--no-alt-screen"];
    const launcher = join(dir, "launch.sh");
    const unset = Object.keys(process.env).filter(key => key.startsWith("CONTEXT_DROP_") || key.startsWith("IMSG_") || key.startsWith("IMESSAGE_")).flatMap(key => ["-u", key]);
    writeFileSync(launcher, `#!/bin/sh\nexport CONTEXT_DROP_CODEX_TOKEN="$(cat ${quote(this.codex.tokenPath)})"\nexec env ${unset.filter((key, i) => key !== "CONTEXT_DROP_CODEX_TOKEN" && !(key === "-u" && unset[i+1] === "CONTEXT_DROP_CODEX_TOKEN")).map(quote).join(" ")} ${argv.map(quote).join(" ")}\n`, { mode: 0o700 });
    chmodSync(launcher, 0o700);
    if (slot.backend === "tmux") {
      const session = this.config.tmuxSession || "context-drop";
      let exists = true;
      try { await this.run("tmux", ["has-session", "-t", session]); } catch { exists = false; }
      const args = exists ? ["new-window", "-d", "-t", session] : ["new-session", "-d", "-s", session];
      const pane = (await this.run("tmux", [...args, "-P", "-F", "#{pane_id}", "-n", `worker-${slot.id}`, "-c", dir, launcher])).trim();
      if (!/^%\d+$/.test(pane)) throw new Error("tmux did not return an exact pane ID");
      slot.pane = pane;
      persist();
      await this.ready(slot);
    } else {
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
      persist();
      await this.herdr(["pane", "run", slot.pane!, quote(launcher)]);
      await this.ready(slot);
    }
  }

  async ready(slot: Slot): Promise<void> {
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      try {
        if (slot.backend === "herdr") {
          const result = JSON.parse(await this.herdr(["agent", "get", slot.pane!]));
          if (result.result?.agent?.agent === "codex") return;
        } else {
          const active = (await this.run("tmux", ["display-message", "-p", "-t", slot.pane!, "#{pane_current_command}"])).trim();
          if (["codex", "node", "dari"].includes(active)) {
            const screen = await this.run("tmux", ["capture-pane", "-p", "-t", slot.pane!, "-S", "-40"]);
            if (screen.includes("OpenAI Codex") && !screen.includes("Error:")) return;
          }
        }
      } catch { /* a new native Codex may not have registered yet */ }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw new Error("native Codex did not register in its pane");
  }

  async alive(slot: Slot): Promise<boolean> {
    if (!slot.pane) return false;
    if (slot.backend === "tmux") {
      const panes = await this.run("tmux", ["list-panes", "-a", "-F", "#{pane_id}"]);
      return panes.split("\n").includes(slot.pane);
    }
    const panes = JSON.parse(await this.herdr(["pane", "list"])).result?.panes;
    if (!Array.isArray(panes)) throw new Error("Herdr returned invalid panes");
    return panes.some((pane: any) => pane.pane_id === slot.pane);
  }

  private async prompt(slot: Slot, text: string): Promise<void> {
    if (!slot.pane) throw new Error("worker has no native pane");
    if (slot.backend === "herdr") await this.herdr(["agent", "prompt", slot.pane, text]);
    else {
      await this.run("tmux", ["send-keys", "-t", slot.pane, "-l", text]);
      await this.run("tmux", ["send-keys", "-t", slot.pane, "Enter"]);
    }
  }
  async submit(slot: Slot, taskId: string, turnId: string): Promise<void> {
    await this.codex.start();
    const task: Task = JSON.parse(readFileSync(join(this.config.stateDir, "workers", String(slot.id), "task.json"), "utf8"));
    if (task.id !== taskId || task.turnId !== turnId) throw new Error("stale task assignment");
    const threadFile = join(this.config.stateDir, "tasks", task.id, "codex.json");
    let threadId: string;
    if (existsSync(threadFile)) threadId = JSON.parse(readFileSync(threadFile, "utf8")).threadId;
    else {
      const { thread: parent } = await this.codex.call("thread/start", { cwd: task.repo, approvalPolicy: "never", sandbox: "danger-full-access", developerInstructions: task.instructions, historyMode: "legacy" });
      await this.codex.call("thread/inject_items", { threadId: parent.id, items: codexHistory(task.session) });
      const { thread } = await this.codex.call("thread/fork", {
        threadId: parent.id, cwd: task.repo, approvalPolicy: "never", sandbox: "danger-full-access", excludeTurns: true,
        developerInstructions: [task.instructions || "", WORKER_PROMPT].join("\n\n"),
        config: { "features.multi_agent": false, "shell_environment_policy.set": { CONTEXT_DROP_RUN_ID: task.id, CONTEXT_DROP_REPORT_URL: `http://${this.config.host}:${this.config.port}/v1/reports`, CONTEXT_DROP_REPORT_CAPABILITY: task.capability } },
      });
      threadId = thread.id;
      writeFileSync(threadFile, JSON.stringify({ threadId, parentThreadId: parent.id, path: thread.path }), { mode: 0o600 });
      await this.codex.call("thread/name/set", { threadId, name: `Worker ${slot.id}: ${task.name}` });
      await this.codex.call("thread/archive", { threadId: parent.id });
    }
    const path = this.assignmentPath(slot.id);
    const previous = existsSync(path) ? JSON.parse(readFileSync(path, "utf8")) : undefined;
    if (previous?.turnId === turnId) { await this.reconcile(slot); return; }
    await this.codex.call("thread/resume", { threadId, excludeTurns: true });
    await this.prompt(slot, `/resume ${threadId}`);
    const { thread: before } = await this.codex.call("thread/read", { threadId, includeTurns: true });
    const assignment = { taskId, turnId, threadId, previousTurnIds: before.turns.map((turn: any) => turn.id), codexTurnId: undefined as string | undefined, reported: false };
    this.assignments.set(slot.id, assignment);
    writeFileSync(path, JSON.stringify(assignment), { mode: 0o600 });
    const { turn } = await this.codex.call("turn/start", { threadId, clientUserMessageId: turnId, input: [{ type: "text", text: task.prompt }], cwd: task.repo });
    assignment.codexTurnId = turn.id;
    writeFileSync(path, JSON.stringify(assignment), { mode: 0o600 });
  }
}
