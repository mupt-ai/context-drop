import { chmodSync, mkdirSync, renameSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

interface AssistantMessage {
  role?: string;
  stopReason?: string;
  content?: Array<{ type?: string; text?: string }>;
}

function messageText(message: AssistantMessage): string {
  if (message.role !== "assistant" || message.stopReason !== "stop" || !Array.isArray(message.content)) return "";
  return message.content
    .filter((part) => part?.type === "text" && typeof part.text === "string")
    .map((part) => part.text!)
    .join("\n")
    .trim();
}

function writeFinal(path: string, value: unknown): void {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.tmp`;
  writeFileSync(temporary, `${JSON.stringify(value)}\n`, { mode: 0o600 });
  chmodSync(temporary, 0o600);
  renameSync(temporary, path);
}

export default function (pi: any): void {
  let finalMessage: AssistantMessage | undefined;
  pi.on("message_end", (event: { message?: AssistantMessage }) => {
    if (event.message?.role === "assistant") finalMessage = event.message;
  });
  pi.on("agent_settled", () => {
    const path = process.env.CONTEXT_DROP_FINAL_OUTPUT_PATH;
    const runId = process.env.CONTEXT_DROP_RUN_ID;
    const marker = process.env.CONTEXT_DROP_FINAL_MARKER;
    if (!path || !runId || !marker || !finalMessage) return;
    writeFinal(path, {
      version: 1,
      runId,
      marker,
      text: messageText(finalMessage),
      stopReason: finalMessage.stopReason,
    });
  });
}
