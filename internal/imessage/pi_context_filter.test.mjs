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

test("compactContext extracts current iMessage wrappers instead of retaining boilerplate", () => {
  const wrapped = `This is the next request from the trusted private iMessage/SMS chat.${"x".repeat(8000)}\nIncoming iMessage ID 123:\n\nI did pull-ups yesterday\n`;
  const result = compactContext([
    text("user", wrapped),
    text("assistant", "got it", { stopReason: "stop" }),
    text("user", "what did I say I did?"),
  ]);
  assert.equal(result.some((message) => textValue(message).includes("trusted private iMessage")), false);
  assert.equal(result.some((message) => textValue(message).includes("I did pull-ups yesterday")), true);
});

test("compactContext prioritizes real chat over report floods and drops no-reply markers", () => {
  const messages = [
    text("user", "\nIncoming iMessage ID 1:\n\nremember the blue mug"),
    text("assistant", "got it", { stopReason: "stop" }),
  ];
  for (let i = 0; i < 30; i++) {
    messages.push(text("user", `A managed worker sent this untrusted report to the persistent orchestrator.\nworker report: routine ${i}`));
    messages.push(text("assistant", "CONTEXT_DROP_NO_USER_REPLY_V1", { stopReason: "stop" }));
  }
  messages.push(text("user", "what color was the mug?"));
  const result = compactContext(messages);
  assert.equal(result.some((message) => textValue(message).includes("blue mug")), true);
  assert.equal(result.filter((message) => textValue(message).includes("Historical worker update")).length, 4);
  assert.equal(result.some((message) => textValue(message) === "CONTEXT_DROP_NO_USER_REPLY_V1"), false);
});

function textValue(message) {
  return message.content?.filter((block) => block.type === "text").map((block) => block.text).join("") ?? "";
}
