import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { main } from "../src/cli.js";
import { expectedSchemaSql, initRelaymuxDb, relaymuxDbPath, relaymuxDbStatus } from "../src/db.js";

function makeIo(env: Record<string, string> = {}) {
  let stdout = "";
  let stderr = "";
  const baseEnv = { ...process.env };
  delete baseEnv.RELAYMUX_SESSION;
  return {
    io: {
      env: { ...baseEnv, ...env },
      stdin: { isTTY: false },
      stdout: { isTTY: false, write: (chunk) => { stdout += String(chunk); } },
      stderr: { write: (chunk) => { stderr += String(chunk); } },
    },
    get stdout() { return stdout; },
    get stderr() { return stderr; },
  };
}

function makeFakeSqliteRunner() {
  let tableExists = false;
  const applied = new Map<number, string>();
  const calls: string[] = [];
  const stdios: any[] = [];

  const runner = (_command, _args, options) => {
    const sql = String(options.input || "");
    calls.push(sql);
    stdios.push(options.stdio);

    if (sql.includes("CREATE TABLE IF NOT EXISTS relaymux_schema_migrations")) {
      tableExists = true;
    }

    if (sql.includes("SELECT name FROM sqlite_master")) {
      return {
        status: 0,
        stdout: tableExists ? "relaymux_schema_migrations\n" : "",
        stderr: "",
      };
    }

    if (sql.includes("SELECT version, name, applied_at FROM relaymux_schema_migrations")) {
      const rows = [...applied.entries()]
        .sort(([left], [right]) => left - right)
        .map(([version, name]) => `${version}\t${name}\t2026-06-18T00:00:00.000Z`)
        .join("\n");
      return { status: 0, stdout: rows ? `${rows}\n` : "", stderr: "" };
    }

    const migrationMatch = sql.match(/INSERT INTO relaymux_schema_migrations\(version, name, applied_at\)\s+VALUES \((\d+), '([^']+)'/);
    if (migrationMatch) {
      applied.set(Number(migrationMatch[1]), migrationMatch[2]);
    }

    return { status: 0, stdout: "", stderr: "" };
  };

  return { runner, calls, stdios };
}

test("relaymux DB path lives under RELAYMUX_HOME", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-db-home-"));
  const home = path.join(root, "home");

  assert.equal(relaymuxDbPath({ RELAYMUX_HOME: home }), path.join(home, "relaymux.sqlite3"));
});

test("SQLite migrations are idempotent through the sqlite runner boundary", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-db-migrate-"));
  const dbPath = path.join(root, "relaymux.sqlite3");
  const { runner, calls, stdios } = makeFakeSqliteRunner();

  const first = initRelaymuxDb({ dbPath, sqlitePath: "/fake/sqlite3", runCommand: runner, env: { PATH: "" } });
  const second = initRelaymuxDb({ dbPath, sqlitePath: "/fake/sqlite3", runCommand: runner, env: { PATH: "" } });

  assert.deepEqual(first.applied.map((migration) => migration.name), ["core_metadata", "runs_events", "runs_schedule"]);
  assert.deepEqual(second.applied, []);
  assert.equal(first.currentVersion, 3);
  assert.equal(second.currentVersion, 3);
  assert.ok(calls.some((sql) => sql.includes("CREATE TABLE IF NOT EXISTS relaymux_metadata")));
  assert.ok(calls.some((sql) => sql.includes("CREATE TABLE IF NOT EXISTS relaymux_runs")));
  assert.ok(calls.some((sql) => sql.includes("CREATE TABLE IF NOT EXISTS relaymux_events")));
  assert.ok(calls.some((sql) => sql.includes("ALTER TABLE relaymux_runs ADD COLUMN schedule_name")));
  assert.ok(stdios.every((stdio) => Array.isArray(stdio) && stdio[0] === "pipe"));
});

test("DB status reports pending migrations for an existing uninitialized DB", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-db-status-"));
  const dbPath = path.join(root, "relaymux.sqlite3");
  fs.writeFileSync(dbPath, "");
  const { runner } = makeFakeSqliteRunner();

  const status = relaymuxDbStatus({ dbPath, sqlitePath: "/fake/sqlite3", runCommand: runner, env: { PATH: "" } });

  assert.equal(status.exists, true);
  assert.equal(status.initialized, false);
  assert.deepEqual(status.pending.map((migration) => migration.name), ["core_metadata", "runs_events", "runs_schedule"]);
});

test("expected schema includes first-party relaymux tables", () => {
  const schema = expectedSchemaSql();

  assert.match(schema, /relaymux_schema_migrations/);
  assert.match(schema, /relaymux_metadata/);
  assert.match(schema, /relaymux_runs/);
  assert.match(schema, /relaymux_events/);
});

test("relaymux db path is script-friendly and does not require sqlite3", async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-db-cli-"));
  const home = path.join(root, "home");
  const harness = makeIo({ RELAYMUX_HOME: home, PATH: "" });
  const code = await main(["db", "path"], harness.io);

  assert.equal(code, 0);
  assert.equal(harness.stdout.trim(), path.join(home, "relaymux.sqlite3"));
  assert.equal(harness.stderr, "");
});

test("relaymux db init fails explicitly when sqlite3 is missing", async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-db-cli-missing-"));
  const harness = makeIo({ RELAYMUX_HOME: path.join(root, "home"), PATH: "" });
  const code = await main(["db", "init"], harness.io);

  assert.equal(code, 1);
  assert.match(harness.stderr, /sqlite3 CLI not found on PATH/);
});

test("relaymux db status reports missing sqlite3 without creating the DB", async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-db-cli-status-"));
  const home = path.join(root, "home");
  const dbPath = path.join(home, "relaymux.sqlite3");
  const harness = makeIo({ RELAYMUX_HOME: home, PATH: "" });
  const code = await main(["db", "status"], harness.io);

  assert.equal(code, 1);
  assert.match(harness.stdout, /sqlite3: missing/);
  assert.match(harness.stdout, new RegExp(dbPath.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.equal(fs.existsSync(dbPath), false);
});
