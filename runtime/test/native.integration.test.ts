import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { createRuntimeServer } from "../src/server.js";
import { command } from "../src/native.js";
import type { RuntimeConfig } from "../src/types.js";

const until = async (predicate: () => boolean) => {
  const end = Date.now() + 60_000;
  while (!predicate()) { if (Date.now() > end) throw new Error("native Codex timed out"); await new Promise(resolve => setTimeout(resolve, 50)); }
};

test("four real Codex TUIs import compaction, execute tools, report, and reuse their panes", { skip: process.env.CONTEXT_DROP_NATIVE_SMOKE !== "1", timeout: 120_000 }, async t => {
  const dir = mkdtempSync("/tmp/context-drop-codex-"), agentDir = join(dir, "codex-home"); mkdirSync(agentDir);
  const requests: any[] = [];
  const provider = createServer(async (req, res) => {
    const chunks: Buffer[] = []; for await (const chunk of req) chunks.push(chunk);
    const input = JSON.parse(Buffer.concat(chunks).toString()); requests.push(input);
    const id = `r-${requests.length}`, messageID = `m-${requests.length}`;
    const tool = requests.length === 1;
    const output = tool ? [{ type: "function_call", id: "fc1", call_id: "call1", name: "exec_command", arguments: JSON.stringify({ cmd: 'printf "run=%s\\n" "$CONTEXT_DROP_RUN_ID"; context-drop report "progress from codex"', max_output_tokens: 100 }) }] : [{ type: "message", id: messageID, role: "assistant", phase: "final_answer", content: [{ type: "output_text", text: "LOCAL_CODEX_OK", annotations: [] }] }];
    const response = { id, object: "response", created_at: 1, status: "completed", model: "fixture", output, usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15 } };
    const event = (type: string, data: any) => `event: ${type}\ndata: ${JSON.stringify({ type, ...data })}\n\n`;
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.end(event("response.created", { response: { ...response, status: "in_progress", output: [] } }) + event("response.output_item.done", { output_index: 0, item: output[0] }) + event("response.completed", { response }));
  });
  await new Promise<void>(resolve => provider.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise<void>(resolve => provider.close(() => resolve())));
  writeFileSync(join(agentDir, "config.toml"), `model="fixture"\nmodel_provider="fixture"\n[model_providers.fixture]\nname="fixture"\nbase_url="http://127.0.0.1:${(provider.address() as any).port}/v1"\nwire_api="responses"\n`);
  const previous = process.env.CODEX_HOME; process.env.CODEX_HOME = agentDir;
  t.after(() => { if (previous === undefined) delete process.env.CODEX_HOME; else process.env.CODEX_HOME = previous; });
  const backend = process.env.CONTEXT_DROP_SMOKE_BACKEND === "herdr" ? "herdr" : "tmux";
  const label = `CD-Codex-test-${Date.now()}`;
  const config: RuntimeConfig = { host: "127.0.0.1", port: 0, stateDir: join(dir, "runtime"), tokenFile: "unused", defaultBackend: backend, tmuxSession: label, fullAIHerdrWorkspaceLabel: label, herdrSession: "default", agents: { codex: { command: [process.env.CONTEXT_DROP_CODEX_PATH || "codex", "--yolo"] } } };
  const server = createRuntimeServer(config, "fixture-token");
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve)); config.port = (server.address() as any).port;
  t.after(async () => {
    await new Promise<void>(resolve => server.close(() => resolve()));
    if (backend === "tmux") await command("tmux", ["kill-session", "-t", label]).catch(() => {});
    else {
      const list = JSON.parse(await command("herdr", ["workspace", "list"]));
      for (const workspace of list.result.workspaces.filter((w: any) => w.label === label)) await command("herdr", ["workspace", "close", workspace.workspace_id]);
    }
    const metadata = join(config.stateDir, "codex-server.json");
    if (existsSync(metadata)) { const { pid } = JSON.parse(readFileSync(metadata, "utf8")); try { process.kill(-pid, "SIGTERM"); } catch {} }
  });
  const source = join(dir, "main.jsonl");
  writeFileSync(source, [
    { type: "session", version: 3, id: "main", cwd: dir },
    { type: "message", id: "old", parentId: null, message: { role: "user", content: [{ type: "text", text: "older context" }] } },
    { type: "compaction", id: "summary", parentId: "old", summary: "COMPACTION_PROOF: native workers required", firstKeptEntryId: "old", tokensBefore: 400 },
    { type: "message", id: "user", parentId: "summary", message: { role: "user", content: [{ type: "text", text: "new context" }] } },
  ].map(x => JSON.stringify(x)).join("\n") + "\n");
  server.pool.setConversation({ path: source, leafId: "user", instructions: "PARENT_AGENTS_STYLE: concise, lowercase." });
  try {
    await server.pool.start(); await until(() => server.pool.workers().every(w => w.ready));
    assert.equal(requests.length, 0, "warming Codex must not call the model");
    const panes = server.pool.workers().map(w => w.paneId);
    const first = server.pool.enqueue({ worker: 1, prompt: "Run the requested check", routerId: "imessage-router", chatId: "fixture" });
    await until(() => first.status === "completed");
    assert.match(JSON.stringify(requests[0]), /COMPACTION_PROOF/);
    assert.match(JSON.stringify(requests[0]), /PARENT_AGENTS_STYLE/);
    assert.match(JSON.stringify(requests[1]), new RegExp(`run=${first.id}`));
    assert.ok(server.pool.state.reports.some(r => r.message === "progress from codex"));
    assert.equal(server.pool.state.reports.at(-1)?.message, "LOCAL_CODEX_OK");
    const second = server.pool.enqueue({ worker: 1, prompt: "Reply again", routerId: "scheduler", chatId: "fixture" });
    await until(() => second.status === "completed");
    assert.deepEqual(server.pool.workers().map(w => w.paneId), panes);
    assert.equal(requests.length, 3);
  } catch (error) {
    for (const w of server.pool.workers()) if (w.paneId) t.diagnostic(await (backend === "tmux" ? command("tmux", ["capture-pane", "-p", "-t", w.paneId, "-S", "-30"]) : command("herdr", ["agent", "read", w.paneId, "--lines", "30"])).catch(() => "unavailable"));
    t.diagnostic(`state: ${dir}; requests: ${requests.length}`); throw error;
  }
});
