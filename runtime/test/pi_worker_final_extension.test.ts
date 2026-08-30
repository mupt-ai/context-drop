import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import register from "../src/pi_worker_final_extension.js";

test("Pi extension atomically persists only the authoritative settled assistant text", () => {
  const handlers = new Map<string, (event?: any) => void>();
  register({ on(name: string, handler: (event?: any) => void) { handlers.set(name, handler); } });
  const output = join(mkdtempSync(join(tmpdir(), "cd-pi-final-")), "final.json");
  const previous = {
    path: process.env.CONTEXT_DROP_FINAL_OUTPUT_PATH,
    run: process.env.CONTEXT_DROP_RUN_ID,
    marker: process.env.CONTEXT_DROP_FINAL_MARKER,
  };
  process.env.CONTEXT_DROP_FINAL_OUTPUT_PATH = output;
  process.env.CONTEXT_DROP_RUN_ID = "run-test";
  process.env.CONTEXT_DROP_FINAL_MARKER = "marker-test";
  try {
    handlers.get("message_end")!({ message: { role: "assistant", stopReason: "toolUse", content: [{ type: "text", text: "progress" }] } });
    handlers.get("message_end")!({ message: { role: "assistant", stopReason: "stop", content: [{ type: "thinking", thinking: "private" }, { type: "text", text: "Final result" }] } });
    handlers.get("agent_settled")!();
    assert.equal(existsSync(output), true);
    assert.deepEqual(JSON.parse(readFileSync(output, "utf8")), {
      version: 1,
      runId: "run-test",
      marker: "marker-test",
      text: "Final result",
      stopReason: "stop",
    });
  } finally {
    if (previous.path === undefined) delete process.env.CONTEXT_DROP_FINAL_OUTPUT_PATH; else process.env.CONTEXT_DROP_FINAL_OUTPUT_PATH = previous.path;
    if (previous.run === undefined) delete process.env.CONTEXT_DROP_RUN_ID; else process.env.CONTEXT_DROP_RUN_ID = previous.run;
    if (previous.marker === undefined) delete process.env.CONTEXT_DROP_FINAL_MARKER; else process.env.CONTEXT_DROP_FINAL_MARKER = previous.marker;
  }
});
