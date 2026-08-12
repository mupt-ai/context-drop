import test from "node:test";
import assert from "node:assert/strict";
import { compactContext } from "./pi_context_filter.mjs";

const text = (role, value, extra = {}) => ({ role, content: [{ type: "text", text: value }], timestamp: 1, ...extra });

test("compactContext preserves the current tool loop and replaces older bulk context", () => {
  const giantWrapper = `trusted context${"x".repeat(20000)}\nThe incoming text:\n\nold question\n`;
  const messages = [
    { role: "compactionSummary", summary: "huge summary".repeat(10000), tokensBefore: 160000, timestamp: 1 },
    text("user", giantWrapper),
    text("assistant", "thinking with tools", { stopReason: "toolUse" }),
    text("toolResult", "large tool result"),
    text("assistant", "old final answer", { stopReason: "stop" }),
    text("user", "current incremental prompt"),
    text("assistant", "current tool call", { stopReason: "toolUse" }),
    text("toolResult", "current tool output"),
  ];
  const result = compactContext(messages);
  assert.equal(result.some((message) => message.role === "compactionSummary"), false);
  assert.equal(result.some((message) => message.role === "toolResult" && textValue(message).includes("large tool result")), false);
  assert.equal(result.some((message) => textValue(message).includes("old final answer")), true);
  assert.equal(result.some((message) => textValue(message).includes("old question")), true);
  assert.equal(result.some((message) => textValue(message).includes("trusted context")), false);
  assert.match(textValue(result.at(-3)), /remains durable/);
  assert.equal(textValue(result.at(-2)), "current tool call");
  assert.equal(textValue(result.at(-1)), "current tool output");
});

test("compactContext leaves a current turn unchanged when no summary is omitted", () => {
  const messages = [text("user", "hello"), text("assistant", "tool", { stopReason: "toolUse" }), text("toolResult", "result")];
  assert.deepEqual(compactContext(messages), messages);
});

function textValue(message) {
  return message.content?.filter((block) => block.type === "text").map((block) => block.text).join("") ?? "";
}
