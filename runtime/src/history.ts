import { readFileSync } from "node:fs";

// Pi is still the messaging adapter. Render its effective branch, including the
// saved compaction summary, as a transcript the worker reads before starting.
export function renderHistory(path: string): string {
  const entries = readFileSync(path, "utf8").trim().split("\n").map(line => JSON.parse(line));
  const compact = entries.findLastIndex(entry => entry.type === "compaction");
  let active = entries;
  if (compact >= 0) {
    const start = entries.findIndex(entry => entry.id === entries[compact].firstKeptEntryId);
    active = [entries[compact], ...(start >= 0 ? entries.slice(start, compact) : []), ...entries.slice(compact + 1)];
  }
  const sections = active.flatMap(entry => {
    if (entry.type === "compaction") return [`## Saved parent conversation summary (historical context)\n\n${entry.summary}`];
    const message = entry.message;
    if (!message) return [];
    const blocks = typeof message.content === "string" ? [{ type: "text", text: message.content }] : message.content || [];
    const lines = blocks.flatMap((block: any) => {
      if (block.type === "text") return [block.text];
      if (block.type === "toolCall") return [`Historical tool call: ${block.name} ${JSON.stringify(block.arguments)}`];
      if (block.type === "image") return ["[image]"];
      return [];
    });
    if (!lines.length) return [];
    const heading = message.role === "assistant" ? "Assistant" : message.role === "toolResult" ? `Historical tool result (${message.toolName || "tool"})` : "User";
    return [`## ${heading}\n\n${lines.join("\n")}`];
  });
  return sections.join("\n\n");
}
