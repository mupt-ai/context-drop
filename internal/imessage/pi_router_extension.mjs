import { Type } from "typebox";

const endpoint = process.env.CONTEXT_DROP_DELEGATE_URL;
const capability = process.env.CONTEXT_DROP_DELEGATE_CAPABILITY;

export default function (pi) {
  pi.registerTool({
    name: "delegate",
    label: "Delegate",
    description: "Launch a visible Context Drop task worker for actionable or non-trivial work. The task must include relevant conversation context and explicit safety gates.",
    parameters: Type.Object({ task: Type.String({ minLength: 1, maxLength: 16000 }) }),
    async execute(_id, { task }, signal) {
      if (!endpoint || !capability) throw new Error("delegation is not configured");
      const response = await fetch(endpoint, {
        method: "POST", signal,
        headers: { authorization: `Bearer ${capability}`, "content-type": "application/json" },
        body: JSON.stringify({ task }),
      });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || `delegate failed (${response.status})`);
      return { content: [{ type: "text", text: `worker ${result.run.id} started visibly in ${result.run.backend} (${result.run.status})` }], details: result };
    },
  });
}
