import { existsSync, readFileSync } from "node:fs";
import type { ParentReportKind, RunRecord, RuntimeConfig } from "./types.js";
import type { CommandRunner } from "./launch.js";
import { readHerdrPane } from "./herdr.js";
import { readTmuxWorker } from "./tmux.js";

const MAX_REPORT_CHARS = 4000;
const TRUNCATION_NOTICE = "\n\n[Final response truncated for delivery.]";
const BEGIN_PREFIX = "CONTEXT_DROP_FINAL_BEGIN_";
const END_PREFIX = "CONTEXT_DROP_FINAL_END_";
const SKIP_PREFIX = "CONTEXT_DROP_FINAL_NO_DELIVERY_";

export interface CapturedWorkerFinal {
  kind: Extract<ParentReportKind, "completed" | "failed">;
  message: string;
  deliverable: boolean;
}

interface PersistedWorkerFinal {
  version?: number;
  runId?: string;
  marker?: string;
  text?: string;
  stopReason?: string;
}

export function finalResponseInstruction(marker: string): string {
  return [
    "FINAL OUTPUT SAFETY NET:",
    "An explicit context-drop report required by TASK remains the primary delivery mechanism.",
    "If no report or other messaging tool delivered the user-facing result, put only the final user-facing response between these exact delimiter lines:",
    `${BEGIN_PREFIX}${marker}`,
    "<final user-facing response>",
    `${END_PREFIX}${marker}`,
    "If a report or another messaging tool already delivered the result, or there is no user-facing result, put this exact line between the delimiters instead:",
    `${SKIP_PREFIX}${marker}`,
    "Do not put progress notes, tool output, shell commands, hidden reasoning, or internal delivery commentary between the delimiters.",
  ].join("\n");
}

function cleanText(text: string): string {
  return text
    .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/\r/g, "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "")
    .trim();
}

function bounded(text: string): string {
  if (text.length <= MAX_REPORT_CHARS) return text;
  return text.slice(0, MAX_REPORT_CHARS - TRUNCATION_NOTICE.length).trimEnd() + TRUNCATION_NOTICE;
}

export function normalizeDeliverableText(text: string): string | undefined {
  const cleaned = cleanText(text);
  if (!cleaned || cleaned === "<final user-facing response>") return undefined;
  const compact = cleaned.replace(/\s+/g, " ").trim();
  const genericStatus = /^(?:done|completed?|finished|sent|success(?:ful(?:ly)?)?|successfully completed the task|ok(?:ay)?|no user-facing (?:reply|result)|nothing to (?:send|report))[.!]?$/i;
  const progress = /^(?:checking|starting|working|investigating|looking|preparing|running|tracing|reviewing)\b|\b(?:i(?:'m| am)|we(?:'re| are)) (?:checking|starting|working|investigating|looking|preparing|running|tracing|reviewing)\b/i;
  if (genericStatus.test(compact) || progress.test(compact)) return undefined;
  return bounded(cleaned);
}

function failed(message: string): CapturedWorkerFinal {
  return { kind: "failed", message, deliverable: false };
}

function skipped(): CapturedWorkerFinal {
  return { kind: "completed", message: "The worker completed after reporting that no daemon delivery was needed.", deliverable: false };
}

export function workerFinalFromPersisted(value: unknown, runId: string, marker: string): CapturedWorkerFinal {
  if (!value || typeof value !== "object") return failed("The worker finished, but its captured final-output record was malformed.");
  const record = value as PersistedWorkerFinal;
  if (record.version !== 1 || record.runId !== runId || record.marker !== marker || typeof record.text !== "string") {
    return failed("The worker finished, but its captured final-output record did not match this run.");
  }
  if (record.stopReason && record.stopReason !== "stop") return failed(`The worker ended with ${record.stopReason} before producing a safe final response.`);
  const begin = `${BEGIN_PREFIX}${marker}`;
  const end = `${END_PREFIX}${marker}`;
  if (record.text.includes(begin) || record.text.includes(end)) return workerFinalFromTerminal(record.text, marker, "done");
  const cleaned = cleanText(record.text);
  if (cleaned === `${SKIP_PREFIX}${marker}` || cleaned === "CONTEXT_DROP_NO_USER_REPLY_V1") return skipped();
  const message = normalizeDeliverableText(cleaned);
  return message ? { kind: "completed", message, deliverable: true } : failed("The worker reached its done state without a suitable final user-facing response.");
}

export function workerFinalFromTerminal(output: string | undefined, marker: string, terminalState: string, readError?: string): CapturedWorkerFinal {
  if (readError) return failed(`The worker reached ${terminalState} state, but its final response could not be captured.`);
  const clean = cleanText(output ?? "");
  const begin = `${BEGIN_PREFIX}${marker}`;
  const end = `${END_PREFIX}${marker}`;
  const beginAt = clean.lastIndexOf(begin);
  const endAt = clean.lastIndexOf(end);
  if (endAt >= 0 && (beginAt < 0 || beginAt > endAt)) return failed("The worker's terminal history was truncated before the beginning of its final response.");
  if (beginAt >= 0 && endAt < beginAt) return failed(`The worker's final response was interrupted before completion (${terminalState}).`);
  if (beginAt >= 0) {
    const body = cleanText(clean.slice(beginAt + begin.length, endAt));
    if (body === `${SKIP_PREFIX}${marker}` || body === "CONTEXT_DROP_NO_USER_REPLY_V1") return skipped();
    const message = normalizeDeliverableText(body);
    return message ? { kind: "completed", message, deliverable: true } : failed("The worker reached its done state without a suitable final user-facing response.");
  }
  return failed(terminalState === "done"
    ? "The worker reached its done state without a suitable final user-facing response."
    : `The worker ${terminalState} before producing a suitable final user-facing response.`);
}

export function captureWorkerFinal(config: RuntimeConfig, run: RunRecord, terminalState: string, runner: CommandRunner): CapturedWorkerFinal {
  if (!run.finalMarker) return failed("The worker reached its terminal state without final-output capture metadata.");
  if (run.finalOutputPath && existsSync(run.finalOutputPath)) {
    try {
      return workerFinalFromPersisted(JSON.parse(readFileSync(run.finalOutputPath, "utf8")), run.id, run.finalMarker);
    } catch {
      return failed("The worker finished, but its captured final-output record could not be read.");
    }
  }
  try {
    const output = run.backend === "herdr"
      ? readHerdrPane(config, run, 500, runner)
      : run.backend === "tmux"
        ? readTmuxWorker(run, runner)
        : undefined;
    return workerFinalFromTerminal(output, run.finalMarker, terminalState);
  } catch {
    return workerFinalFromTerminal(undefined, run.finalMarker, terminalState, "capture failed");
  }
}
