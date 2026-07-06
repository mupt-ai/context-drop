import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { applyHomeMigration, buildHomeMigrationInventory, ensureRelaymuxHomeLayout, migrateConfigObject } from "../src/migration.js";

function makeLayout() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-migrate-"));
  return {
    root,
    home: path.join(root, "home"),
    legacyConfigPath: path.join(root, "xdg", "relaymux", "config.json"),
    legacyStateDir: path.join(root, "state", "relaymux"),
    orchestratorImessageDir: path.join(root, "orchestrator-imessage"),
    agentmuxConfigPath: path.join(root, "agentmux", "config.json"),
    agentmuxStateDir: path.join(root, "agentmux-state"),
  };
}

test("migrateConfigObject rewrites only managed legacy relaymux paths", () => {
  const migrated = migrateConfigObject({
    stateDir: "~/.local/state/relaymux",
    daemon: {
      tokenFile: "~/.local/state/relaymux/webhook-token",
      logDir: "~/.local/state/relaymux/logs",
    },
    orchestrator: {
      command: ["pi", "--session-dir", "~/.local/state/relaymux/sessions", "{prompt}"],
    },
  }, { homeDir: "/tmp/relaymux-home", env: { RELAYMUX_HOME: "/tmp/relaymux-home" } });

  assert.equal(migrated.stateDir, "/tmp/relaymux-home/state");
  assert.equal(migrated.daemon.tokenFile, "/tmp/relaymux-home/state/webhook-token");
  assert.equal(migrated.daemon.logDir, "/tmp/relaymux-home/logs");
  assert.deepEqual(migrated.orchestrator.command, ["pi", "--session-dir", "/tmp/relaymux-home/state/sessions", "{prompt}"]);
});

test("migration inventory finds only relaymux-owned legacy state", () => {
  const layout = makeLayout();
  fs.mkdirSync(path.dirname(layout.legacyConfigPath), { recursive: true });
  fs.writeFileSync(layout.legacyConfigPath, JSON.stringify({ stateDir: "~/.local/state/relaymux", imessage: {}, daemon: {} }));
  fs.mkdirSync(layout.legacyStateDir, { recursive: true });
  fs.writeFileSync(path.join(layout.legacyStateDir, "runs.jsonl"), "{}\n");
  fs.writeFileSync(path.join(layout.legacyStateDir, "webhook-token"), "do-not-print\n", { mode: 0o644 });

  const inventory = buildHomeMigrationInventory({
    homeDir: layout.home,
    legacyConfigPath: layout.legacyConfigPath,
    legacyStateDir: layout.legacyStateDir,
    orchestratorImessageDir: layout.orchestratorImessageDir,
    agentmuxConfigPath: layout.agentmuxConfigPath,
    agentmuxStateDir: layout.agentmuxStateDir,
  }, { RELAYMUX_HOME: layout.home });

  assert.ok(inventory.items.some((item) => item.operation === "migrate-config" && item.source === layout.legacyConfigPath));
  assert.ok(inventory.items.some((item) => item.source.endsWith("webhook-token") && item.secret));
});

test("ensureRelaymuxHomeLayout creates only state and logs directories", () => {
  const layout = makeLayout();

  ensureRelaymuxHomeLayout(layout.home);

  assert.equal(fs.statSync(path.join(layout.home, "state")).isDirectory(), true);
  assert.equal(fs.statSync(path.join(layout.home, "logs")).isDirectory(), true);
  for (const name of ["tasks", "reports", "research", "personal", "implementation-notes"]) {
    assert.equal(fs.existsSync(path.join(layout.home, name)), false, name);
  }
});

test("applyHomeMigration copies config, state, logs, and keeps tokens private", () => {
  const layout = makeLayout();
  fs.mkdirSync(path.dirname(layout.legacyConfigPath), { recursive: true });
  fs.writeFileSync(layout.legacyConfigPath, JSON.stringify({
    stateDir: "~/.local/state/relaymux",
    imessage: {},
    daemon: {
      tokenFile: "~/.local/state/relaymux/webhook-token",
      logDir: "~/.local/state/relaymux/logs",
    },
  }));
  fs.mkdirSync(path.join(layout.legacyStateDir, "logs"), { recursive: true });
  fs.writeFileSync(path.join(layout.legacyStateDir, "webhook-token"), "secret\n", { mode: 0o644 });
  fs.writeFileSync(path.join(layout.legacyStateDir, "logs", "daemon.out.log"), "started\n");

  const env = { RELAYMUX_HOME: layout.home };
  const inventory = buildHomeMigrationInventory({
    homeDir: layout.home,
    legacyConfigPath: layout.legacyConfigPath,
    legacyStateDir: layout.legacyStateDir,
    orchestratorImessageDir: layout.orchestratorImessageDir,
    agentmuxConfigPath: layout.agentmuxConfigPath,
    agentmuxStateDir: layout.agentmuxStateDir,
  }, env);
  const results = applyHomeMigration(inventory, { env });

  assert.ok(results.some((result) => result.status === "copied"));
  const config = JSON.parse(fs.readFileSync(path.join(layout.home, "config.json"), "utf8"));
  assert.equal(config.stateDir, path.join(layout.home, "state"));
  assert.equal(config.daemon.tokenFile, path.join(layout.home, "state", "webhook-token"));
  assert.equal(config.daemon.logDir, path.join(layout.home, "logs"));
  assert.equal(fs.readFileSync(path.join(layout.home, "state", "webhook-token"), "utf8"), "secret\n");
  assert.equal(fs.statSync(path.join(layout.home, "state", "webhook-token")).mode & 0o777, 0o600);
  assert.equal(fs.readFileSync(path.join(layout.home, "logs", "daemon.out.log"), "utf8"), "started\n");
});
