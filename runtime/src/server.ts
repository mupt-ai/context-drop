import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { timingSafeEqual } from "node:crypto";
import { appendFileSync, existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { LaunchRequest, RunRecord, RuntimeConfig } from "./types.js";
import { launchInHerdr } from "./herdr.js";
import { systemRunner, type CommandRunner } from "./launch.js";
import { launchInTmux } from "./tmux.js";

function json(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "content-type": "application/json", "x-content-type-options": "nosniff" }); res.end(JSON.stringify(body));
}
function authorized(req: IncomingMessage, token: string): boolean {
  const got = req.headers.authorization?.replace(/^Bearer\s+/i, "") ?? "";
  const a = Buffer.from(got), b = Buffer.from(token);
  return a.length === b.length && a.length > 0 && timingSafeEqual(a, b);
}
async function body(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []; let size = 0;
  for await (const chunk of req) { const b = Buffer.from(chunk); size += b.length; if (size > 64 * 1024) throw new Error("request too large"); chunks.push(b); }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}
function runsPath(config: RuntimeConfig): string { return resolve(config.stateDir, "runs.jsonl"); }
export function loadRuns(config: RuntimeConfig): RunRecord[] {
  if (!existsSync(runsPath(config))) return [];
  const records = new Map<string, RunRecord>();
  for (const line of readFileSync(runsPath(config), "utf8").split("\n")) { if (!line.trim()) continue; try { const r = JSON.parse(line) as RunRecord; records.set(r.id, r); } catch {} }
  return [...records.values()].sort((a,b) => b.createdAt.localeCompare(a.createdAt));
}

export function createRuntimeServer(config: RuntimeConfig, token: string, runner: CommandRunner = systemRunner) {
  if (config.host !== "127.0.0.1" && config.host !== "::1") throw new Error("runtime must bind to loopback");
  return createServer(async (req, res) => {
    try {
      if (!authorized(req, token)) { json(res, 401, { error: "unauthorized" }); return; }
      if (req.method === "GET" && req.url === "/health") { json(res, 200, { ok: true }); return; }
      if (req.method === "GET" && req.url === "/v1/agents") {
        const agents = Object.entries(config.agents).map(([name, a]) => ({ name, command: a.command[0], prompt_mode: a.promptMode ?? "arg" })); json(res, 200, { agents }); return;
      }
      if (req.method === "POST" && req.url === "/v1/runs") {
        const input = await body(req) as LaunchRequest;
        if (typeof input?.agent !== "string" || typeof input?.repo !== "string" || typeof input?.prompt !== "string" || (input.name !== undefined && typeof input.name !== "string") || (input.backend !== undefined && input.backend !== "tmux" && input.backend !== "herdr") || (input.workspaceId !== undefined && typeof input.workspaceId !== "string")) throw new Error("invalid launch request");
        const id = `run_${Date.now().toString(36)}_${Math.random().toString(36).slice(2,10)}`;
        const backend = input.backend ?? config.defaultBackend ?? "tmux";
        const run = backend === "herdr" ? launchInHerdr(config, input, id, runner) : launchInTmux(config, input, id, runner);
        appendFileSync(runsPath(config), JSON.stringify(run) + "\n", { mode: 0o600 }); json(res, 201, run); return;
      }
      if (req.method === "GET" && req.url === "/v1/runs") { json(res, 200, { runs: loadRuns(config) }); return; }
      if (req.method === "GET" && req.url?.startsWith("/v1/runs/")) {
        const id = decodeURIComponent(req.url.slice("/v1/runs/".length)); const run = loadRuns(config).find(r => r.id === id);
        if (!run) { json(res, 404, { error: "not found" }); return; } json(res, 200, run); return;
      }
      json(res, 404, { error: "not found" });
    } catch (err) { json(res, 400, { error: err instanceof Error ? err.message : "bad request" }); }
  });
}
