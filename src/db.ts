import fs from "node:fs";
import path from "node:path";

import { defaultRelaymuxDatabasePath, ensureDirectory } from "./paths.js";
import { runCommand } from "./process.js";

export const RELAYMUX_DB_MIGRATIONS = [
  {
    version: 1,
    name: "core_metadata",
    statements: [
      `CREATE TABLE IF NOT EXISTS relaymux_metadata (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)`,
    ],
  },
  {
    version: 2,
    name: "runs_events",
    statements: [
      `CREATE TABLE IF NOT EXISTS relaymux_runs (
  run_id TEXT PRIMARY KEY,
  started_at TEXT NOT NULL,
  agent TEXT,
  name TEXT,
  session TEXT,
  session_mode TEXT,
  session_source TEXT,
  target TEXT,
  window_target TEXT,
  repo TEXT,
  workdir TEXT,
  prompt_file TEXT,
  script_file TEXT,
  command TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}'
)`,
      "CREATE INDEX IF NOT EXISTS relaymux_runs_started_at_idx ON relaymux_runs(started_at)",
      "CREATE INDEX IF NOT EXISTS relaymux_runs_agent_idx ON relaymux_runs(agent)",
      `CREATE TABLE IF NOT EXISTS relaymux_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT,
  time TEXT NOT NULL,
  event TEXT NOT NULL,
  message TEXT,
  exit_code INTEGER,
  payload_json TEXT NOT NULL DEFAULT '{}'
)`,
      "CREATE INDEX IF NOT EXISTS relaymux_events_run_id_idx ON relaymux_events(run_id)",
      "CREATE INDEX IF NOT EXISTS relaymux_events_time_idx ON relaymux_events(time)",
    ],
  },
  {
    version: 3,
    name: "runs_schedule",
    statements: [
      "ALTER TABLE relaymux_runs ADD COLUMN schedule_name TEXT",
      "CREATE INDEX IF NOT EXISTS relaymux_runs_schedule_name_idx ON relaymux_runs(schedule_name)",
      "ALTER TABLE relaymux_events ADD COLUMN schedule_name TEXT",
    ],
  },
];

export function relaymuxDbPath(env = process.env) {
  return defaultRelaymuxDatabasePath(env);
}

export function findSqliteCli(env = process.env) {
  return findExecutable("sqlite3", env);
}

export function initRelaymuxDb(options: any = {}) {
  const env = options.env || process.env;
  const dbPath = options.dbPath || relaymuxDbPath(env);
  const sqlitePath = resolveSqlitePath(options, env);
  const runner = options.runCommand || runCommand;

  ensureDirectory(path.dirname(dbPath));
  runSqlite(sqlitePath, dbPath, bootstrapSql(), { runner, env });

  const before = readAppliedMigrations({ dbPath, sqlitePath, runner, env });
  const appliedByVersion = new Map<number, any>(before.map((migration) => [migration.version, migration]));
  const applied: any[] = [];

  for (const migration of RELAYMUX_DB_MIGRATIONS) {
    const existing = appliedByVersion.get(migration.version);
    if (existing) {
      if (existing.name !== migration.name) {
        throw new Error(`SQLite migration version ${migration.version} is named ${existing.name}, expected ${migration.name}`);
      }
      continue;
    }

    runSqlite(sqlitePath, dbPath, migrationSql(migration), { runner, env });
    applied.push({ version: migration.version, name: migration.name });
  }

  writeSchemaVersionMetadata({ dbPath, sqlitePath, runner, env });
  const migrations = readAppliedMigrations({ dbPath, sqlitePath, runner, env });
  return {
    dbPath,
    sqlitePath,
    applied,
    migrations,
    currentVersion: currentVersion(migrations),
    expectedVersion: expectedVersion(),
  };
}

export function relaymuxDbStatus(options: any = {}) {
  const env = options.env || process.env;
  const dbPath = options.dbPath || relaymuxDbPath(env);
  const sqlitePath = options.sqlitePath || findSqliteCli(env);
  const exists = fs.existsSync(dbPath);
  const status: any = {
    dbPath,
    sqlite: {
      available: Boolean(sqlitePath),
      path: sqlitePath || "",
    },
    exists,
    initialized: false,
    migrations: [],
    currentVersion: 0,
    expectedVersion: expectedVersion(),
    pending: RELAYMUX_DB_MIGRATIONS.map(({ version, name }) => ({ version, name })),
    error: "",
  };

  if (!sqlitePath || !exists) {
    return status;
  }

  const runner = options.runCommand || runCommand;
  try {
    const hasTable = queryScalar(sqlitePath, dbPath, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'relaymux_schema_migrations';", { runner, env });
    if (hasTable !== "relaymux_schema_migrations") {
      return status;
    }

    const migrations = readAppliedMigrations({ dbPath, sqlitePath, runner, env });
    const appliedVersions = new Set(migrations.map((migration) => migration.version));
    status.initialized = true;
    status.migrations = migrations;
    status.currentVersion = currentVersion(migrations);
    status.pending = RELAYMUX_DB_MIGRATIONS
      .filter((migration) => !appliedVersions.has(migration.version))
      .map(({ version, name }) => ({ version, name }));
  } catch (error) {
    status.error = error.message;
  }

  return status;
}

export function expectedSchemaSql() {
  const chunks = [bootstrapSql().trim()];
  for (const migration of RELAYMUX_DB_MIGRATIONS) {
    chunks.push(`-- migration ${migration.version}: ${migration.name}`);
    chunks.push(migration.statements.map((statement) => `${statement};`).join("\n"));
  }
  return `${chunks.join("\n\n")}\n`;
}

function resolveSqlitePath(options, env) {
  const sqlitePath = options.sqlitePath || findSqliteCli(env);
  if (!sqlitePath) {
    throw new Error(`sqlite3 CLI not found on PATH; install sqlite3 to use relaymux db commands. DB path: ${relaymuxDbPath(env)}`);
  }
  return sqlitePath;
}

function bootstrapSql() {
  return [
    ".bail on",
    "PRAGMA journal_mode = WAL;",
    `CREATE TABLE IF NOT EXISTS relaymux_schema_migrations (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  name TEXT NOT NULL UNIQUE,
  applied_at TEXT NOT NULL
);`,
  ].join("\n");
}

function migrationSql(migration) {
  return [
    ".bail on",
    "PRAGMA foreign_keys = ON;",
    "BEGIN IMMEDIATE;",
    ...migration.statements.map((statement) => `${statement};`),
    `INSERT INTO relaymux_schema_migrations(version, name, applied_at)
VALUES (${migration.version}, '${escapeSql(migration.name)}', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));`,
    "COMMIT;",
  ].join("\n");
}

function writeSchemaVersionMetadata({ dbPath, sqlitePath, runner, env }) {
  runSqlite(sqlitePath, dbPath, [
    ".bail on",
    `INSERT INTO relaymux_metadata(key, value, updated_at)
VALUES ('schema_version', '${expectedVersion()}', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;`,
  ].join("\n"), { runner, env });
}

function readAppliedMigrations({ dbPath, sqlitePath, runner, env }) {
  const output = query(sqlitePath, dbPath, [
    ".mode tabs",
    "SELECT version, name, applied_at FROM relaymux_schema_migrations ORDER BY version;",
  ].join("\n"), { runner, env });

  return output
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [version, name, appliedAt] = line.split("\t");
      return {
        version: Number(version),
        name,
        appliedAt,
      };
    })
    .filter((migration) => Number.isFinite(migration.version) && migration.name);
}

function queryScalar(sqlitePath, dbPath, sql, options) {
  return query(sqlitePath, dbPath, sql, options).trim();
}

function query(sqlitePath, dbPath, sql, { runner, env }) {
  return runSqlite(sqlitePath, dbPath, sql, { runner, env }).stdout;
}

function runSqlite(sqlitePath, dbPath, sql, { runner, env }) {
  const result = runner(sqlitePath, ["-batch", dbPath], {
    input: sql,
    env,
    allowFailure: true,
    stdio: ["pipe", "pipe", "pipe"],
  });
  if (result.status !== 0 || result.error) {
    const detail = result.stderr?.trim() || result.error?.message || `sqlite3 exited with ${result.status}`;
    throw new Error(detail);
  }
  return result;
}

function currentVersion(migrations) {
  return migrations.reduce((max, migration) => Math.max(max, migration.version), 0);
}

function expectedVersion() {
  return RELAYMUX_DB_MIGRATIONS[RELAYMUX_DB_MIGRATIONS.length - 1]?.version || 0;
}

function escapeSql(value) {
  return String(value).replace(/'/g, "''");
}

function sqlValue(value) {
  return value === undefined || value === null ? "NULL" : `'${escapeSql(String(value))}'`;
}

function sqlJson(value) {
  return sqlValue(JSON.stringify(value ?? {}));
}

// Resolve a usable sqlite runner + path for state mirroring. Returns null when
// sqlite3 is missing or the DB has not been initialized, so callers can no-op
// gracefully without failing launch/status.
export function resolveStateDb({ dbPath, env = process.env, sqlitePath, runCommand: runner }: { dbPath?: string; env?: any; sqlitePath?: string; runCommand?: any } = {}) {
  const resolvedDb = dbPath || relaymuxDbPath(env);
  const resolvedSqlite = sqlitePath || findSqliteCli(env);
  if (!resolvedSqlite || !fs.existsSync(resolvedDb)) return null;
  const status = relaymuxDbStatus({ dbPath: resolvedDb, sqlitePath: resolvedSqlite, runCommand: runner, env });
  if (!status.initialized || status.pending.length > 0) return null;
  return { dbPath: resolvedDb, sqlitePath: resolvedSqlite, runner: runner || runCommand };
}

export function upsertRunInDb(handle, run) {
  if (!handle) return false;
  const payload = { ...run };
  delete payload.runId;
  const sql = [
    ".bail on",
    "BEGIN IMMEDIATE;",
    `INSERT INTO relaymux_runs (
  run_id, started_at, agent, name, session, session_mode, session_source,
  target, window_target, repo, workdir, prompt_file, script_file, command,
  schedule_name, payload_json
) VALUES (
  ${sqlValue(run.runId)},
  ${sqlValue(run.time)},
  ${sqlValue(run.agent)},
  ${sqlValue(run.name)},
  ${sqlValue(run.session)},
  ${sqlValue(run.sessionMode)},
  ${sqlValue(run.sessionSource)},
  ${sqlValue(run.target)},
  ${sqlValue(run.windowTarget)},
  ${sqlValue(run.repo)},
  ${sqlValue(run.workdir)},
  ${sqlValue(run.promptFile)},
  ${sqlValue(run.scriptFile)},
  ${sqlValue(run.command)},
  ${sqlValue(run.scheduleName)},
  ${sqlJson(payload)}
)
ON CONFLICT(run_id) DO UPDATE SET
  started_at = excluded.started_at,
  agent = excluded.agent,
  name = excluded.name,
  session = excluded.session,
  session_mode = excluded.session_mode,
  session_source = excluded.session_source,
  target = excluded.target,
  window_target = excluded.window_target,
  repo = excluded.repo,
  workdir = excluded.workdir,
  prompt_file = excluded.prompt_file,
  script_file = excluded.script_file,
  command = excluded.command,
  schedule_name = excluded.schedule_name,
  payload_json = excluded.payload_json;`,
    "COMMIT;",
  ].join("\n");
  runSqlite(handle.sqlitePath, handle.dbPath, sql, { runner: handle.runner, env: process.env });
  return true;
}

export function insertEventInDb(handle, event) {
  if (!handle) return false;
  const payload = { ...event };
  delete payload.runId;
  delete payload.event;
  delete payload.time;
  delete payload.message;
  delete payload.exitCode;
  delete payload.scheduleName;
  const sql = [
    ".bail on",
    "BEGIN IMMEDIATE;",
    `INSERT INTO relaymux_events (
  run_id, time, event, message, exit_code, schedule_name, payload_json
) VALUES (
  ${sqlValue(event.runId)},
  ${sqlValue(event.time)},
  ${sqlValue(event.event)},
  ${sqlValue(event.message)},
  ${event.exitCode === undefined ? "NULL" : Number(event.exitCode)},
  ${sqlValue(event.scheduleName)},
  ${sqlJson(payload)}
);`,
    "COMMIT;",
  ].join("\n");
  runSqlite(handle.sqlitePath, handle.dbPath, sql, { runner: handle.runner, env: process.env });
  return true;
}

// Reap prior schedule runs directly in SQLite when the DB is available. Returns
// the reaped run ids so the caller can also mirror them into JSONL.
export function reapScheduleRunsInDb(handle, scheduleName, { exceptRunId, time = new Date().toISOString() }: { exceptRunId?: string; time?: string } = {}) {
  if (!handle || !scheduleName) return [];
  const prior = query(handle.sqlitePath, handle.dbPath, [
    ".mode tabs",
    `SELECT run_id FROM relaymux_runs WHERE schedule_name = '${escapeSql(scheduleName)}'`,
    exceptRunId ? ` AND run_id <> '${escapeSql(exceptRunId)}'` : "",
    ";",
  ].join(""), { runner: handle.runner, env: process.env });
  const runIds = prior.split("\n").map((line) => line.trim()).filter(Boolean);
  const reaped = [];
  for (const runId of runIds) {
    const event = {
      time,
      runId,
      event: "reaped",
      message: "superseded by schedule relaunch",
      reason: "schedule-reuse",
      scheduleName,
    };
    insertEventInDb(handle, event);
    reaped.push(event);
  }
  return reaped;
}

function findExecutable(command, env) {
  if (!command) return null;
  if (command.includes(path.sep)) {
    try {
      fs.accessSync(command, fs.constants.X_OK);
      return command;
    } catch {
      return null;
    }
  }

  for (const dir of String(env.PATH || "").split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, command);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      return candidate;
    } catch {
      // Keep searching PATH.
    }
  }
  return null;
}
