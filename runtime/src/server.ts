import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { timingSafeEqual, randomBytes } from "node:crypto";
import { existsSync, readFileSync, mkdirSync, writeFileSync, chmodSync, renameSync } from "node:fs";
import { resolve } from "node:path";
import type { LaunchRequest, ParentReport, RunRecord, RuntimeConfig, ParentReportKind } from "./types.js";
import { launchInHerdr } from "./herdr.js";
import { systemRunner, type CommandRunner } from "./launch.js";
import { launchInTmux } from "./tmux.js";

const REPORT_KINDS = new Set<ParentReportKind>(["started", "progress", "needs_user", "completed", "failed"]);
function json(res: ServerResponse, status: number, body: unknown): void { res.writeHead(status, { "content-type": "application/json", "x-content-type-options": "nosniff" }); res.end(JSON.stringify(body)); }
function body(req: IncomingMessage): Promise<unknown> { return new Promise((resolveBody, reject) => { const chunks: Buffer[] = []; let size = 0; req.on("data", (chunk: Buffer) => { size += chunk.length; if (size > 64 * 1024) reject(new Error("request too large")); else chunks.push(chunk); }); req.on("end", () => { try { resolveBody(JSON.parse(Buffer.concat(chunks).toString("utf8"))); } catch { reject(new Error("invalid JSON")); } }); req.on("error", reject); }); }
const MAX_RECORDS = 1000;
function append<T>(path: string, value: T): void { const all = records<T>(path); all.push(value); const bounded = all.slice(-MAX_RECORDS); const temp = `${path}.tmp`; writeFileSync(temp, bounded.map(v => JSON.stringify(v)).join("\n") + "\n", { mode: 0o600 }); chmodSync(temp, 0o600); renameSync(temp, path); }
function records<T>(path: string): T[] { if (!existsSync(path)) return []; const out: T[] = []; for (const line of readFileSync(path, "utf8").split("\n")) { if (!line.trim()) continue; try { out.push(JSON.parse(line) as T); } catch {} } return out; }
function replaceRecords<T extends { id: string }>(path: string, values: T[]): void { const temp = `${path}.tmp`; writeFileSync(temp, values.slice(-MAX_RECORDS).map(v => JSON.stringify(v)).join("\n") + (values.length ? "\n" : ""), { mode: 0o600 }); chmodSync(temp, 0o600); renameSync(temp, path); }
function runsPath(config: RuntimeConfig): string { return resolve(config.stateDir, "runs.jsonl"); }
export function loadRuns(config: RuntimeConfig): RunRecord[] { const result = new Map<string, RunRecord>(); for (const r of records<RunRecord>(runsPath(config))) result.set(r.id, r); return [...result.values()].sort((a,b) => b.createdAt.localeCompare(a.createdAt)); }
function tasksPath(config: RuntimeConfig): string { return resolve(config.stateDir, "parent-tasks.jsonl"); }
function reportsPath(config: RuntimeConfig): string { return resolve(config.stateDir, "parent-reports.jsonl"); }
function capabilityPath(config: RuntimeConfig): string { return resolve(config.stateDir, "delegate-capability"); }
function capability(config: RuntimeConfig): string { if (!existsSync(capabilityPath(config))) { mkdirSync(config.stateDir, { recursive: true, mode: 0o700 }); writeFileSync(capabilityPath(config), randomBytes(32).toString("base64url") + "\n", { mode: 0o600 }); chmodSync(capabilityPath(config), 0o600); } return readFileSync(capabilityPath(config), "utf8").trim(); }
function capMatches(got: string, expected: string): boolean { const a = Buffer.from(got), b = Buffer.from(expected); return a.length > 0 && a.length === b.length && timingSafeEqual(a, b); }
function workerCwd(config: RuntimeConfig): string { const dir = resolve(config.stateDir, "..", "delegation", "workers"); mkdirSync(dir, { recursive: true, mode: 0o700 }); chmodSync(dir, 0o700); return dir; }
function reportExtension(): string { return new URL("./report_to_parent_extension.js", import.meta.url).pathname; }
function workerPrompt(task: string): string { return `You are a Context Drop task worker operating visibly on behalf of an iMessage session. Complete this task end to end using your ordinary tools. Report with report_to_parent: started when beginning, progress at meaningful milestones, needs_user when blocked on a user decision, completed with a concise result, or failed with a precise reason.\n\nSAFETY GATES: require explicit, unambiguous confirmation in the chat before making payments or purchases, resetting passwords or MFA/account recovery, or accepting/materially changing terms, contracts, or subscriptions. Report needs_user and wait at those gates.\n\nTASK:\n${task}`; }

export function createRuntimeServer(config: RuntimeConfig, token: string, runner: CommandRunner = systemRunner) {
  if (config.host !== "127.0.0.1" && config.host !== "::1") throw new Error("runtime must bind to loopback");
  mkdirSync(config.stateDir, { recursive: true, mode: 0o700 });
  return createServer(async (req, res) => {
    try {
      const auth = req.headers.authorization?.replace(/^Bearer\s+/i, "") ?? "";
      const general = capMatches(auth, token);
      if (req.method === "GET" && req.url === "/health") { if (!general) { json(res, 401, { error: "unauthorized" }); return; } json(res, 200, { ok: true }); return; }
      if (req.method === "GET" && req.url === "/v1/delegate-capability") { if (!general) { json(res, 401, { error: "unauthorized" }); return; } json(res, 200, { capability: capability(config) }); return; }
      if (req.method === "GET" && req.url === "/v1/agents") { if (!general) { json(res, 401, { error: "unauthorized" }); return; } json(res, 200, { agents: Object.entries(config.agents).map(([name, a]) => ({ name, command: a.command[0], prompt_mode: a.promptMode ?? "arg" })) }); return; }
      if (req.method === "POST" && req.url === "/v1/delegate") {
        if (!general && !capMatches(auth, capability(config))) { json(res, 401, { error: "unauthorized" }); return; }
        const input = await body(req) as { task?: string; chatID?: string };
        if (!input?.task || input.task.length > 16000) throw new Error("task is required and must be <= 16000 bytes");
        const id = `run_${Date.now().toString(36)}_${randomBytes(5).toString("hex")}`;
        const reportCap = randomBytes(32).toString("base64url");
        const request: LaunchRequest = { agent: config.delegateAgent ?? "pi", repo: workerCwd(config), prompt: workerPrompt(input.task), name: `task-${id.slice(-10)}`, backend: config.defaultBackend, extension: reportExtension(), environment: { CONTEXT_DROP_REPORT_URL: `http://${config.host}:${config.port}/v1/reports`, CONTEXT_DROP_REPORT_CAPABILITY: reportCap, CONTEXT_DROP_RUN_ID: id } };
        const run = request.backend === "herdr" ? launchInHerdr(config, request, id, runner) : launchInTmux(config, request, id, runner);
        append(runsPath(config), run);
        append(tasksPath(config), { id: `task_${id}`, runId: id, chatID: input.chatID ?? "", task: input.task, reportCapability: reportCap, createdAt: run.createdAt });
        json(res, 201, { taskId: `task_${id}`, run }); return;
      }
      if (req.method === "POST" && req.url === "/v1/runs") {
        if (!general) { json(res, 401, { error: "unauthorized" }); return; }
        const input = await body(req) as LaunchRequest;
        if (typeof input?.agent !== "string" || typeof input?.repo !== "string" || typeof input?.prompt !== "string" || (input.name !== undefined && typeof input.name !== "string") || (input.backend !== undefined && input.backend !== "tmux" && input.backend !== "herdr") || (input.workspaceId !== undefined && typeof input.workspaceId !== "string") || (input.environment !== undefined && typeof input.environment !== "object")) throw new Error("invalid launch request");
        const id = `run_${Date.now().toString(36)}_${randomBytes(5).toString("hex")}`;
        const backend = input.backend ?? config.defaultBackend ?? "tmux";
        const run = backend === "herdr" ? launchInHerdr(config, input, id, runner) : launchInTmux(config, input, id, runner);
        append(runsPath(config), run); json(res, 201, run); return;
      }
      if (req.method === "POST" && req.url === "/v1/reports") {
        const input = await body(req) as { runId?: string; kind?: ParentReportKind; message?: string };
        if (!input.runId || !REPORT_KINDS.has(input.kind as ParentReportKind) || !input.message || input.message.length > 4000) throw new Error("invalid report");
        const task = records<any>(tasksPath(config)).find(t => t.runId === input.runId); if (!task || (!general && !capMatches(auth, task.reportCapability))) { json(res, 401, { error: "unauthorized" }); return; }
        const report: ParentReport = { id: `report_${Date.now().toString(36)}_${randomBytes(5).toString("hex")}`, runId: input.runId, kind: input.kind!, message: input.message, createdAt: new Date().toISOString() }; append(reportsPath(config), report); json(res, 201, { report }); return;
      }
      if (req.method === "GET" && req.url === "/v1/reports") { if (!general) { json(res, 401, { error: "unauthorized" }); return; } json(res, 200, { reports: records<ParentReport>(reportsPath(config)).filter(r => !r.claimedAt) }); return; }
      if (req.method === "POST" && req.url?.startsWith("/v1/reports/") && req.url.endsWith("/deliver")) {
        if (!general) { json(res, 401, { error: "unauthorized" }); return; } const id = decodeURIComponent(req.url.slice("/v1/reports/".length, -"/deliver".length)); const all = records<ParentReport>(reportsPath(config)); const report = all.find(r => r.id === id); if (!report) { json(res, 404, { error: "not found" }); return; } if (report.claimedAt) { json(res, 409, { error: "already delivered" }); return; } report.claimedAt = new Date().toISOString(); replaceRecords(reportsPath(config), all); json(res, 200, { report }); return;
      }
      if (req.method === "GET" && req.url === "/v1/delegations") { if (!general) { json(res, 401, { error: "unauthorized" }); return; } const tasks = records<any>(tasksPath(config)); const reports = records<ParentReport>(reportsPath(config)); const out = tasks.map(t => { const terminal = reports.filter(r => r.runId === t.runId && (r.kind === "completed" || r.kind === "failed")).sort((a,b) => b.createdAt.localeCompare(a.createdAt))[0]; return { id: t.id, runId: t.runId, chatID: t.chatID, task: t.task, status: terminal?.kind ?? "running", createdAt: t.createdAt }; }); json(res, 200, { tasks: out }); return; }
      if (req.method === "GET" && req.url === "/v1/runs") { if (!general) { json(res, 401, { error: "unauthorized" }); return; } json(res, 200, { runs: loadRuns(config) }); return; }
      if (req.method === "GET" && req.url?.startsWith("/v1/runs/")) { if (!general) { json(res, 401, { error: "unauthorized" }); return; } const id = decodeURIComponent(req.url.slice("/v1/runs/".length)); const run = loadRuns(config).find(r => r.id === id); if (!run) { json(res, 404, { error: "not found" }); return; } json(res, 200, run); return; }
      json(res, 404, { error: "not found" });
    } catch (err) { json(res, 400, { error: err instanceof Error ? err.message : "bad request" }); }
  });
}
