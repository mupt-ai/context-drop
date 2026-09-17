import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomBytes } from "node:crypto";
import { WorkspaceDirectory } from "./workspaces.js";
import { WorkerPool, matches } from "./pool.js";
import type { RuntimeConfig, Task } from "./types.js";

function json(res: ServerResponse, status: number, value: unknown) {
  res.writeHead(status, { "content-type": "application/json", "x-content-type-options": "nosniff" });
  res.end(JSON.stringify(value));
}
async function body(req: IncomingMessage): Promise<any> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 512 * 1024) throw new Error("request too large");
    chunks.push(Buffer.from(chunk));
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
}
const publicTask = (task: Task, pool: WorkerPool) => ({ runId: task.id, worker: task.worker, paneId: task.worker ? pool.state.slots[task.worker - 1].pane || "" : "", agent: pool.config.workerAgent, name: task.name, status: task.status, fullyManaged: true, selected: false, workspaceTarget: task.workspaceTarget });

export function createRuntimeServer(config: RuntimeConfig, token: string, pool = new WorkerPool(config), workspaces = new WorkspaceDirectory(config)) {
  if (!["127.0.0.1", "::1"].includes(config.host) || !token) throw new Error("runtime requires loopback and a private token");
  let router: { capability: string; routerId: string; chatId: string } | undefined;
  const server = createServer(async (req, res) => {
    try {
      const path = new URL(req.url || "/", "http://runtime").pathname;
      const bearer = req.headers.authorization?.replace(/^Bearer\s+/i, "") || "";
      const general = matches(bearer, token);
      const owner = router && matches(bearer, router.capability) ? router : undefined;
      if (req.method === "POST" && path === "/v1/worker-events") {
        pool.event(bearer, await body(req));
        return json(res, 200, { ok: true });
      }
      if (req.method === "POST" && path === "/v1/reports") {
        const report = pool.reportWithToken(bearer, await body(req));
        return json(res, 201, { report });
      }
      if (!general && !owner) return json(res, 401, { error: "unauthorized" });
      if (req.method === "GET" && path === "/health") return json(res, 200, { ok: true, workers: pool.workers() });
      if (req.method === "POST" && path === "/v1/router-capabilities" && general) {
        const input = await body(req);
        if (!input.routerId || !input.chatId) throw new Error("routerId and chatId are required");
        router = { routerId: input.routerId, chatId: input.chatId, capability: randomBytes(32).toString("base64url") };
        return json(res, 201, { capability: router.capability });
      }
      if (req.method === "POST" && path === "/v1/conversation" && owner) {
        pool.setConversation(await body(req));
        return json(res, 200, { ok: true });
      }
      if (req.method === "GET" && path === "/v1/workers") return json(res, 200, { workers: pool.workers(), queued: pool.state.tasks.filter(task => task.status === "queued").length });
      if (req.method === "GET" && path === "/v1/workspaces" && owner) return json(res, 200, { workspaces: await workspaces.discover() });
      if (req.method === "POST" && path === "/v1/workspaces/delegate" && owner) {
        const input = await body(req);
        if (input.newTask !== undefined && typeof input.newTask !== "boolean") throw new Error("newTask must be boolean");
        const workspaceTarget = await workspaces.resolve(input);
        if (input.conversation) pool.setConversation(input.conversation);
        const task = pool.enqueue({ worker: input.worker, prompt: input.prompt, repo: workspaceTarget.cwd, workspaceTarget, routerId: owner.routerId, chatId: owner.chatId, requestId: input.requestId, newTask: input.newTask });
        return json(res, 201, { task: publicTask(task, pool) });
      }
      if (req.method === "POST" && path === "/v1/workers/delegate" && owner) {
        const input = await body(req);
        if (input.conversation) pool.setConversation(input.conversation);
        if (input.newTask !== undefined && typeof input.newTask !== "boolean") throw new Error("newTask must be boolean");
        const task = pool.enqueue({ worker: input.worker, prompt: input.prompt, routerId: owner.routerId, chatId: owner.chatId, requestId: input.requestId, newTask: input.newTask });
        return json(res, 201, { task: publicTask(task, pool) });
      }
      if (req.method === "POST" && path === "/v1/tasks/schedule" && general) {
        const input = await body(req);
        if (!input.chatId || input.routerId !== "scheduler") throw new Error("schedule owner is required");
        const task = pool.enqueue({ prompt: input.prompt, name: input.name, repo: input.repo, routerId: input.routerId, chatId: input.chatId, requestId: input.requestId });
        return json(res, 201, { runId: task.id, task: publicTask(task, pool) });
      }
      if (req.method === "POST" && path === "/v1/reports/lease" && general) {
        const input = await body(req);
        return json(res, 200, { report: pool.lease(input.routerId, input.chatId, input.leaseSeconds) });
      }
      const finish = /^\/v1\/reports\/([^/]+)\/(ack|release)$/.exec(path);
      if (req.method === "POST" && finish && general) {
        pool.finish(finish[1], await body(req), finish[2] === "ack");
        return json(res, 200, { ok: true });
      }
      if (req.method === "GET" && path === "/v1/agents" && general) return json(res, 200, { agents: [{ name: config.workerAgent, command: config.agents[config.workerAgent]?.command[0] || config.workerAgent, prompt_mode: "herdr" }] });
      if (req.method === "GET" && path === "/v1/live-tasks" && general) return json(res, 200, { backend: "herdr", tasks: pool.state.tasks.filter(task => ["queued", "running", "waiting"].includes(task.status)).map(task => publicTask(task, pool)) });
      if (req.method === "GET" && path === "/v1/runs" && general) return json(res, 200, { runs: pool.state.tasks.map(task => ({ id: task.id, ...publicTask(task, pool), repo: task.repo, createdAt: task.createdAt })) });
      return json(res, 404, { error: "not found" });
    } catch (error) {
      const message = error instanceof Error ? error.message : "request failed";
      return json(res, message.startsWith("unauthorized") ? 401 : 400, { error: message });
    }
  });
  server.on("close", () => pool.close());
  return Object.assign(server, { pool });
}
