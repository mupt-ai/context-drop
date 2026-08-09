import { Type } from "typebox";
import { StringEnum } from "@earendil-works/pi-ai";

const endpoint = process.env.CONTEXT_DROP_REPORT_URL;
const capability = process.env.CONTEXT_DROP_REPORT_CAPABILITY;
const runId = process.env.CONTEXT_DROP_RUN_ID;

export default function (pi: any) {
  pi.registerTool({
    name: "report_to_parent",
    label: "Report to parent",
    description: "Durably report task status to the parent iMessage router. Report before claiming completion and whenever user input is required.",
    parameters: Type.Object({
      kind: StringEnum(["started", "progress", "needs_user", "completed", "failed"] as const),
      message: Type.String({ minLength: 1, maxLength: 4000 }),
    }),
    async execute(_id: string, params: { kind: string; message: string }, signal: AbortSignal) {
      if (!endpoint || !capability || !runId) throw new Error("parent reporting is not configured");
      const response = await fetch(endpoint, {
        method: "POST", signal,
        headers: { authorization: `Bearer ${capability}`, "content-type": "application/json" },
        body: JSON.stringify({ runId, ...params }),
      });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || `parent report failed (${response.status})`);
      return { content: [{ type: "text", text: `report ${result.report.id} persisted` }], details: result };
    },
  });
}
