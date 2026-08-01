import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { spawnSync } from "node:child_process";

import { initRelaymuxDb, relaymuxDbStatus } from "../src/db.js";
import { recordRun, recordEvent, reapScheduleRuns, readRuns, readEvents } from "../src/state.js";

const SQLITE_AVAILABLE = (() => {
  try {
    return spawnSync("sqlite3", ["--version"]).status === 0;
  } catch {
    return false;
  }
})();

function setupRealDb(root: string) {
  const home = path.join(root, "home");
  const dbPath = path.join(home, "relaymux.sqlite3");
  const stateDir = path.join(home, "state");
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(stateDir, { recursive: true });
  initRelaymuxDb({ dbPath, env: { PATH: process.env.PATH || "" } });
  return { home, dbPath, stateDir };
}

function queryDb(dbPath: string, sql: string): string {
  const result = spawnSync("sqlite3", ["-batch", dbPath], {
    input: sql,
    maxBuffer: 4 * 1024 * 1024,
  });
  if (result.status !== 0) {
    throw new Error(`sqlite3 query failed: ${String(result.stderr || result.stdout)}`);
  }
  return String(result.stdout || "");
}

test("DB status reports the new runs_schedule migration after init", { skip: !SQLITE_AVAILABLE }, () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-statedb-status-"));
  const { dbPath } = setupRealDb(root);
  const status = relaymuxDbStatus({ dbPath, env: { PATH: process.env.PATH || "" } });
  assert.equal(status.initialized, true);
  assert.equal(status.currentVersion, 3);
  assert.deepEqual(status.pending, []);
});

test("recordRun and recordEvent mirror into the SQLite store when available", { skip: !SQLITE_AVAILABLE }, () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-statedb-mirror-"));
  const { dbPath, stateDir } = setupRealDb(root);

  recordRun(stateDir, {
    time: "2026-01-01T00:00:00Z",
    runId: "run-1",
    session: "agents",
    name: "personal-digest-hourly",
    agent: "custom",
    scheduleName: "personal-digest-hourly",
  });
  recordEvent(stateDir, { time: "2026-01-01T00:00:01Z", runId: "run-1", event: "started" });

  const runRow = queryDb(dbPath, "SELECT run_id, schedule_name FROM relaymux_runs WHERE run_id = 'run-1';").trim();
  assert.match(runRow, /run-1\|personal-digest-hourly/);
  const eventCount = Number(queryDb(dbPath, "SELECT COUNT(*) FROM relaymux_events WHERE run_id = 'run-1';").trim());
  assert.ok(eventCount >= 1);
  // JSONL compatibility files are still written.
  assert.equal(readRuns(stateDir).length, 1);
  assert.equal(readEvents(stateDir).length, 1);
});

test("reapScheduleRuns mirrors reaped events into SQLite and JSONL", { skip: !SQLITE_AVAILABLE }, () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-statedb-reap-"));
  const { dbPath, stateDir } = setupRealDb(root);

  recordRun(stateDir, { time: "t1", runId: "r1", name: "s", scheduleName: "daily" });
  recordRun(stateDir, { time: "t2", runId: "r2", name: "s", scheduleName: "daily" });

  const reaped = reapScheduleRuns(stateDir, "daily", { exceptRunId: "r-current" });

  assert.equal(reaped.length, 2);
  assert.ok(reaped.every((e) => e.event === "reaped"));
  const reapedCount = Number(queryDb(dbPath, "SELECT COUNT(*) FROM relaymux_events WHERE event = 'reaped';").trim());
  assert.ok(reapedCount >= 2);
  const jsonlReaped = readEvents(stateDir).filter((e) => e.event === "reaped");
  assert.equal(jsonlReaped.length, 2);
});

test("state mirroring is best-effort and never throws when sqlite3 is absent", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-statedb-noop-"));
  const stateDir = path.join(root, "state");
  fs.mkdirSync(stateDir, { recursive: true });
  const originalPath = process.env.PATH;
  process.env.PATH = "";
  try {
    assert.doesNotThrow(() => recordRun(stateDir, { time: "t", runId: "r", name: "n" }));
    assert.doesNotThrow(() => recordEvent(stateDir, { time: "t", runId: "r", event: "started" }));
    assert.doesNotThrow(() => reapScheduleRuns(stateDir, "daily", { exceptRunId: "r" }));
    assert.equal(readRuns(stateDir).length, 1);
    assert.equal(readEvents(stateDir).length, 1);
  } finally {
    process.env.PATH = originalPath;
  }
});
