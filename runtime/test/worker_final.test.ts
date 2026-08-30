import test from "node:test";
import assert from "node:assert/strict";
import { finalResponseInstruction, normalizeDeliverableText, workerFinalFromPersisted, workerFinalFromTerminal } from "../src/worker_final.js";

const runId = "run-breakfast";
const marker = "marker123";

test("ordinary final assistant text is normalized when Pi persisted the settled message", () => {
  assert.deepEqual(workerFinalFromPersisted({
    version: 1,
    runId,
    marker,
    text: "What did you eat and when?",
    stopReason: "stop",
  }, runId, marker), {
    kind: "completed",
    message: "What did you eat and when?",
    deliverable: true,
  });
});

test("progress, generic completion chatter, and interrupted output are not deliverable", () => {
  for (const text of ["I'm checking the records now.", "Done.", "Successfully completed the task."]) {
    const result = workerFinalFromPersisted({ version: 1, runId, marker, text, stopReason: "stop" }, runId, marker);
    assert.equal(result.kind, "failed", text);
    assert.equal(result.deliverable, false, text);
  }
  const interrupted = workerFinalFromPersisted({ version: 1, runId, marker, text: "useful", stopReason: "error" }, runId, marker);
  assert.equal(interrupted.kind, "failed");
  assert.equal(interrupted.deliverable, false);
});

test("terminal fallback accepts only a complete marked response and honors explicit no-delivery", () => {
  const output = `tool chatter\nCONTEXT_DROP_FINAL_BEGIN_${marker}\nA safe final answer.\nCONTEXT_DROP_FINAL_END_${marker}\nprompt chrome`;
  assert.deepEqual(workerFinalFromTerminal(output, marker, "done"), {
    kind: "completed",
    message: "A safe final answer.",
    deliverable: true,
  });
  assert.deepEqual(workerFinalFromTerminal(`CONTEXT_DROP_FINAL_BEGIN_${marker}\nCONTEXT_DROP_FINAL_NO_DELIVERY_${marker}\nCONTEXT_DROP_FINAL_END_${marker}`, marker, "done"), {
    kind: "completed",
    message: "The worker completed after reporting that no daemon delivery was needed.",
    deliverable: false,
  });
  assert.equal(workerFinalFromTerminal(`CONTEXT_DROP_FINAL_BEGIN_${marker}\npartial`, marker, "done").kind, "failed");
  assert.match(finalResponseInstruction(marker), new RegExp(`CONTEXT_DROP_FINAL_BEGIN_${marker}[\\s\\S]*CONTEXT_DROP_FINAL_END_${marker}`));
});

test("deliverable normalization rejects progress but keeps substantive completion text", () => {
  assert.equal(normalizeDeliverableText("I'm checking the records now."), undefined);
  assert.equal(normalizeDeliverableText("Source collection is complete; I'm checking the final record now."), undefined);
  assert.equal(normalizeDeliverableText("Successfully completed the task."), undefined);
  assert.equal(normalizeDeliverableText("Finished recording breakfast; 460 calories were logged."), "Finished recording breakfast; 460 calories were logged.");
});

test("persisted records are scoped to one exact run and marker", () => {
  const wrong = workerFinalFromPersisted({ version: 1, runId: "other", marker, text: "secret", stopReason: "stop" }, runId, marker);
  assert.equal(wrong.kind, "failed");
  assert.equal(wrong.deliverable, false);
});
