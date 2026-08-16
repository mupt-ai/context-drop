import { Type } from "typebox";

const endpoint = process.env.CONTEXT_DROP_DELEGATE_URL;
const capability = process.env.CONTEXT_DROP_DELEGATE_CAPABILITY;
const base = endpoint?.replace(/\/v1\/tasks\/delegate$/, "");
async function request(path, method, body, signal) {
  if (!base || !capability) throw new Error("Context Drop runtime is not configured");
  const response = await fetch(base + path, { method, signal, headers: { authorization: `Bearer ${capability}`, "content-type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || `Context Drop request failed (${response.status})`);
  return result;
}

export default function (pi) {
  pi.registerTool({
    name: "list_tasks", label: "List tasks",
    description: "Get authoritative live status from the configured backend. Use for every question about running work; never answer from memory.",
    parameters: Type.Object({}),
    async execute(_id, _input, signal) { const result = await request("/v1/tasks", "GET", undefined, signal); return { content: [{ type: "text", text: JSON.stringify(result) }], details: result }; },
  });
  pi.registerTool({
    name: "delegate_task", label: "Delegate task",
    description: "Start ordinary work in a fully managed full-AI worker. The optional agent must be configured; keep the private name short and recognizable.",
    parameters: Type.Object({ agent: Type.Optional(Type.String({ minLength: 1, maxLength: 64 })), prompt: Type.String({ minLength: 1, maxLength: 16000 }), name: Type.Optional(Type.String({ minLength: 1, maxLength: 120 })) }),
    async execute(_id, input, signal) { const result = await request("/v1/tasks/delegate", "POST", input, signal); return { content: [{ type: "text", text: `task started in pane ${result.task.paneId}` }], details: result }; },
  });
  pi.registerTool({
    name: "continue_task", label: "Continue task",
    description: "Send a relevant follow-up to the exact live worker pane. Use only a paneId obtained from list_tasks or a trusted worker report; never guess.",
    parameters: Type.Object({ paneId: Type.String({ minLength: 1, maxLength: 128 }), prompt: Type.String({ minLength: 1, maxLength: 16000 }) }),
    async execute(_id, input, signal) { const result = await request("/v1/tasks/continue", "POST", input, signal); return { content: [{ type: "text", text: `follow-up sent to pane ${input.paneId}` }], details: result }; },
  });
  pi.registerTool({
    name: "herdr_overview", label: "Herdr overview",
    description: "Get authoritative workspace, tab, pane, agent, cwd, and lifecycle topology from the configured explicit Herdr session.",
    parameters: Type.Object({}),
    async execute(_id, _input, signal) { const result = await request("/v1/herdr/overview", "GET", undefined, signal); return { content: [{ type: "text", text: JSON.stringify(result) }], details: result }; },
  });
  pi.registerTool({
    name: "herdr_read", label: "Read Herdr agent",
    description: "Read recent output directly from an exact live Herdr agent pane; never launch an inspector worker.",
    parameters: Type.Object({ paneId: Type.String({ minLength: 1, maxLength: 128 }), lines: Type.Optional(Type.Number({ minimum: 1, maximum: 500 })) }),
    async execute(_id, input, signal) { const result = await request("/v1/herdr/read", "POST", { paneId: input.paneId, lines: input.lines ?? 120 }, signal); return { content: [{ type: "text", text: result.output }], details: result }; },
  });
  pi.registerTool({
    name: "herdr_prompt", label: "Prompt Herdr agent",
    description: "Prompt an exact existing Herdr agent pane through the native agent API. Resolve the pane first and never guess.",
    parameters: Type.Object({ paneId: Type.String({ minLength: 1, maxLength: 128 }), prompt: Type.String({ minLength: 1, maxLength: 16000 }) }),
    async execute(_id, input, signal) { const result = await request("/v1/herdr/prompt", "POST", input, signal); return { content: [{ type: "text", text: `prompt sent to pane ${input.paneId}` }], details: result }; },
  });
  pi.registerTool({
    name: "herdr_wait", label: "Wait for Herdr agent",
    description: "Wait for an exact pane lifecycle state: idle, working, blocked, done, or unknown.",
    parameters: Type.Object({ paneId: Type.String({ minLength: 1, maxLength: 128 }), statuses: Type.Array(Type.String({ minLength: 1, maxLength: 16 }), { minItems: 1, maxItems: 5 }), timeoutMs: Type.Number({ minimum: 1, maximum: 300000 }) }),
    async execute(_id, input, signal) { const result = await request("/v1/herdr/wait", "POST", input, signal); return { content: [{ type: "text", text: JSON.stringify(result) }], details: result }; },
  });
  pi.registerTool({
    name: "repo_list", label: "List repositories",
    description: "List private validated repository aliases available for launching. Only aliases are returned; do not guess missing aliases.",
    parameters: Type.Object({}),
    async execute(_id, _input, signal) { const result = await request("/v1/repos", "GET", undefined, signal); return { content: [{ type: "text", text: JSON.stringify(result) }], details: result }; },
  });
  pi.registerTool({
    name: "start_agent", label: "Start Herdr agent",
    description: "Start a configured agent using exactly one validated repoAlias or a workspaceId whose live cwd resolves uniquely. Never guess ambiguous targets.",
    parameters: Type.Object({ agent: Type.String({ minLength: 1, maxLength: 64 }), name: Type.String({ minLength: 1, maxLength: 120 }), prompt: Type.String({ minLength: 1, maxLength: 16000 }), repoAlias: Type.Optional(Type.String({ minLength: 1, maxLength: 120 })), workspaceId: Type.Optional(Type.String({ minLength: 1, maxLength: 128 })) }),
    async execute(_id, input, signal) { const result = await request("/v1/herdr/start", "POST", input, signal); return { content: [{ type: "text", text: `agent started in pane ${result.task.paneId}` }], details: result }; },
  });
  pi.on("before_agent_start", () => {
    pi.setActiveTools(["list_tasks", "delegate_task", "continue_task", "herdr_overview", "herdr_read", "herdr_prompt", "herdr_wait", "repo_list", "start_agent"]);
  });
}
