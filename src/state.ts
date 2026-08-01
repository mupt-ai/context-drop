import fs from "node:fs";
import path from "node:path";

import { ensureDirectory } from "./paths.js";
import { insertEventInDb, resolveStateDb, upsertRunInDb, reapScheduleRunsInDb } from "./db.js";

function dbHandleFromStateDir(stateDir) {
  if (!stateDir) return null;
  const home = path.dirname(stateDir);
  const dbPath = path.join(home, "relaymux.sqlite3");
  try {
    return resolveStateDb({ dbPath });
  } catch {
    return null;
  }
}

export function writePromptFile(stateDir, runId, prompt) {
  const dir = path.join(stateDir, "prompts");
  ensureDirectory(dir);
  const file = path.join(dir, `${runId}.txt`);
  fs.writeFileSync(file, prompt);
  return file;
}

export function writeScriptFile(stateDir, runId, script) {
  const dir = path.join(stateDir, "scripts");
  ensureDirectory(dir);
  const file = path.join(dir, `${runId}.sh`);
  fs.writeFileSync(file, script, { mode: 0o700 });
  return file;
}

export function recordRun(stateDir, run) {
  appendJsonl(path.join(stateDir, "runs.jsonl"), run);
  try {
    upsertRunInDb(dbHandleFromStateDir(stateDir), run);
  } catch {
    // SQLite mirroring is best-effort; JSONL remains the compatibility source.
  }
}

export function recordEvent(stateDir, event) {
  appendJsonl(path.join(stateDir, "events.jsonl"), event);
  try {
    insertEventInDb(dbHandleFromStateDir(stateDir), event);
  } catch {
    // SQLite mirroring is best-effort; JSONL remains the compatibility source.
  }
}

export function readRuns(stateDir) {
  return readJsonl(path.join(stateDir, "runs.jsonl"));
}

export function readEvents(stateDir) {
  return readJsonl(path.join(stateDir, "events.jsonl"));
}

export function latestEventsByRun(stateDir) {
  const latest = new Map();
  for (const event of readEvents(stateDir)) {
    latest.set(event.runId, event);
  }
  return latest;
}

// Mark every prior run record for a schedule as reaped so `relaymux status`
// stops showing zombie rows for superseded ticks. Only runs without an existing
// terminal event (completed/reaped) are touched, and the current runId is kept.
export function reapScheduleRuns(stateDir, scheduleName, { exceptRunId, time = new Date().toISOString() }: { exceptRunId?: string; time?: string } = {}) {
  if (!scheduleName) return [];
  const handle = dbHandleFromStateDir(stateDir);
  // Prefer reaping in SQLite (canonical) when available, and mirror the same
  // events into JSONL for status/compatibility. Fall back to JSONL-only reaping.
  let dbReaped: any[] = [];
  if (handle) {
    try {
      dbReaped = reapScheduleRunsInDb(handle, scheduleName, { exceptRunId, time });
    } catch {
      dbReaped = [];
    }
  }

  const events = readEvents(stateDir);
  const terminal = new Set(
    events
      .filter((event) => event.event === "completed" || event.event === "reaped")
      .map((event) => event.runId),
  );
  const reaped = [];
  for (const event of dbReaped) {
    if (terminal.has(event.runId)) continue;
    appendJsonl(path.join(stateDir, "events.jsonl"), event);
    reaped.push(event);
  }

  for (const run of readRuns(stateDir)) {
    if (run.scheduleName !== scheduleName) continue;
    if (exceptRunId && run.runId === exceptRunId) continue;
    if (terminal.has(run.runId)) continue;
    if (reaped.some((event) => event.runId === run.runId)) continue;
    const event = {
      time,
      runId: run.runId,
      event: "reaped",
      message: "superseded by schedule relaunch",
      reason: "schedule-reuse",
      scheduleName,
    };
    appendJsonl(path.join(stateDir, "events.jsonl"), event);
    try {
      insertEventInDb(handle, event);
    } catch {}
    reaped.push(event);
  }
  return reaped;
}

function appendJsonl(file, value) {
  ensureDirectory(path.dirname(file));
  fs.appendFileSync(file, `${JSON.stringify(value)}\n`);
}

function readJsonl(file) {
  if (!fs.existsSync(file)) {
    return [];
  }

  return fs
    .readFileSync(file, "utf8")
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      try {
        return JSON.parse(line);
      } catch {
        return null;
      }
    })
    .filter(Boolean);
}
