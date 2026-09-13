import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { chmodSync, existsSync, mkdirSync, openSync, closeSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { randomBytes } from "node:crypto";
import WebSocket from "ws";
import type { RuntimeConfig } from "./types.js";

export class CodexClient {
  private socket?: WebSocket;
  private starting?: Promise<void>;
  private nextID = 0;
  private pending = new Map<number, { resolve: (value: any) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>();
  onNotification?: (method: string, params: any) => void;
  address = "";
  tokenPath: string;
  private token = "";
  constructor(private config: RuntimeConfig) { this.tokenPath = join(config.stateDir, "codex-server.token"); }
  async start(): Promise<void> {
    if (this.socket?.readyState === WebSocket.OPEN) return;
    return this.starting ??= this.connect().finally(() => { this.starting = undefined; });
  }
  private async connect(): Promise<void> {
    mkdirSync(this.config.stateDir, { recursive: true, mode: 0o700 });
    const metadata = join(this.config.stateDir, "codex-server.json");
    let state: { address: string; pid?: number } | undefined;
    if (existsSync(metadata)) state = JSON.parse(readFileSync(metadata, "utf8"));
    if (!existsSync(this.tokenPath)) writeFileSync(this.tokenPath, randomBytes(32).toString("base64url"), { mode: 0o600 });
    this.token = readFileSync(this.tokenPath, "utf8").trim();
    if (!state) {
      const server = createServer();
      await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
      const port = (server.address() as { port: number }).port;
      await new Promise<void>(resolve => server.close(() => resolve()));
      state = { address: `ws://127.0.0.1:${port}` };
    }
    this.address = state.address;
    let alive = false;
    if (state.pid) { try { process.kill(state.pid, 0); alive = true; } catch (error) { if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error; } }
    if (!alive) {
      const [program, ...args] = this.config.agents.codex?.command || [];
      if (!program) throw new Error("worker pool requires Codex; configure agents.codex.command");
      const log = openSync(join(this.config.stateDir, "codex-server.log"), "a", 0o600);
      const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("CONTEXT_DROP_") && !key.startsWith("IMESSAGE_") && !key.startsWith("IMSG_")));
      const child = spawn(program, [...args, "app-server", "--listen", this.address, "--ws-auth", "capability-token", "--ws-token-file", this.tokenPath, "--disable", "multi_agent"], { detached: true, env, stdio: ["ignore", log, log] });
      closeSync(log);
      await new Promise<void>((resolve, reject) => { child.once("spawn", resolve); child.once("error", reject); });
      child.unref();
      state.pid = child.pid;
      writeFileSync(metadata, JSON.stringify(state), { mode: 0o600 });
      chmodSync(metadata, 0o600);
    }
    let last: unknown;
    for (let i = 0; i < 150; i++) {
      try {
        const socket = new WebSocket(this.address, { headers: { Authorization: `Bearer ${this.token}` }, handshakeTimeout: 1000 });
        await new Promise<void>((resolve, reject) => { socket.once("open", resolve); socket.once("error", reject); });
        this.socket = socket;
        socket.on("message", data => {
          const message = JSON.parse(data.toString());
          const pending = this.pending.get(message.id);
          if (pending) {
            clearTimeout(pending.timer); this.pending.delete(message.id);
            if (message.error) pending.reject(new Error(message.error.message)); else pending.resolve(message.result);
          } else if (message.method && message.id === undefined) this.onNotification?.(message.method, message.params);
        });
        socket.on("error", () => {});
        socket.on("close", () => { if (this.socket !== socket) return; this.socket = undefined; for (const p of this.pending.values()) { clearTimeout(p.timer); p.reject(new Error("Codex connection closed")); } this.pending.clear(); });
        await this.call("initialize", { clientInfo: { name: "context_drop", version: "1" }, capabilities: { experimentalApi: true } });
        socket.send(JSON.stringify({ method: "initialized", params: {} }));
        return;
      } catch (error) { last = error; await new Promise(resolve => setTimeout(resolve, 100)); }
    }
    throw new Error(`Codex app server unavailable: ${String(last)}`);
  }
  call(method: string, params: any): Promise<any> {
    if (this.socket?.readyState !== WebSocket.OPEN) return Promise.reject(new Error("Codex is disconnected"));
    return new Promise((resolve, reject) => {
      const id = ++this.nextID;
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`Codex ${method} timed out`)); }, 30_000);
      this.pending.set(id, { resolve, reject, timer });
      this.socket!.send(JSON.stringify({ id, method, params }));
    });
  }
  close(): void { this.socket?.close(); }
}

// Pi is still the messaging adapter. Import its effective branch, including the
// saved compaction summary, without asking a model to summarize it again.
export function codexHistory(path: string): any[] {
  const entries = readFileSync(path, "utf8").trim().split("\n").map(line => JSON.parse(line));
  const compact = entries.findLastIndex(entry => entry.type === "compaction");
  let active = entries;
  if (compact >= 0) {
    const start = entries.findIndex(entry => entry.id === entries[compact].firstKeptEntryId);
    active = [entries[compact], ...(start >= 0 ? entries.slice(start, compact) : []), ...entries.slice(compact + 1)];
  }
  return active.flatMap(entry => {
    if (entry.type === "compaction") return [{ type: "message", role: "user", content: [{ type: "input_text", text: `Saved parent conversation summary (historical context):\n${entry.summary}` }] }];
    const message = entry.message;
    if (!message) return [];
    const role = message.role === "assistant" ? "assistant" : "user";
    const blocks = typeof message.content === "string" ? [{ type: "text", text: message.content }] : message.content || [];
    const content = blocks.flatMap((block: any) => {
      if (block.type === "text") return [{ type: role === "assistant" ? "output_text" : "input_text", text: message.role === "toolResult" ? `Historical tool result (${message.toolName || "tool"}):\n${block.text}` : block.text }];
      if (block.type === "toolCall") return [{ type: "output_text", text: `Historical tool call: ${block.name} ${JSON.stringify(block.arguments)}` }];
      if (block.type === "image" && role === "user") return [{ type: "input_image", image_url: `data:${block.mimeType};base64,${block.data}` }];
      return [];
    });
    return content.length ? [{ type: "message", role, content }] : [];
  });
}
