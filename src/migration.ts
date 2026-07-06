import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { defaultConfigPath, legacyDefaultConfigPath, legacyDefaultStateDir } from "./config.js";
import { defaultRelaymuxHome, expandPath, ensureDirectory } from "./paths.js";

const HOME_SUBDIRS = ["state", "logs"];
const STATE_RELAYMUX_NAMES = new Set([
  "daemon-state.json",
  "events.jsonl",
  "runs.jsonl",
  "webhook-token",
  "prompts",
  "scripts",
  "sessions",
  "logs",
]);
const ORCHESTRATOR_IMESSAGE_NAMES = new Set([
  "daemon-state.json",
  "events.jsonl",
  "runs.jsonl",
  "webhook-token",
  "token",
  "loops",
  "state.json",
  "session.json",
  "sessions",
  "prompts",
  "scripts",
  "logs",
  "daemon.out.log",
  "daemon.err.log",
  "out.log",
  "err.log",
]);

export function ensureRelaymuxHomeLayout(homeDir = defaultRelaymuxHome()) {
  ensureDirectory(homeDir);
  for (const dir of HOME_SUBDIRS) {
    ensureDirectory(path.join(homeDir, dir));
  }
  return homeDir;
}

export function buildHomeMigrationInventory(options: any = {}, env = process.env) {
  const homeDir = expandPath(options.homeDir || defaultRelaymuxHome(env));
  const stateDir = path.join(homeDir, "state");
  const logsDir = path.join(homeDir, "logs");
  const targetConfigPath = expandPath(options.targetConfigPath || path.join(homeDir, "config.json"));
  const items = [];
  const seen = new Set();

  const addItem = (item) => {
    if (!item?.source) return;
    const source = expandPath(item.source);
    const destination = item.destination ? expandPath(item.destination) : "";
    const key = `${source}\0${destination}\0${item.operation || "copy"}`;
    if (seen.has(key)) return;
    seen.add(key);
    items.push({
      operation: item.operation || "copy",
      kind: item.kind || pathKind(source),
      source,
      destination,
      reason: item.reason || "relaymux-managed legacy path",
      secret: Boolean(item.secret),
      symlinkSafe: item.symlinkSafe !== false,
    });
  };

  const legacyConfig = expandPath(options.legacyConfigPath || legacyDefaultConfigPath(env));
  if (fileExists(legacyConfig) && !samePath(legacyConfig, targetConfigPath) && isRelaymuxConfigFile(legacyConfig)) {
    addItem({
      operation: "migrate-config",
      kind: "file",
      source: legacyConfig,
      destination: targetConfigPath,
      reason: "legacy relaymux config (~/.config/relaymux/config.json)",
      secret: false,
    });
  }

  if (options.configPath && fileExists(options.configPath) && !samePath(options.configPath, targetConfigPath) && isRelaymuxConfigFile(options.configPath)) {
    addItem({
      operation: "migrate-config",
      kind: "file",
      source: options.configPath,
      destination: targetConfigPath,
      reason: "explicit relaymux config outside ~/.relaymux",
      secret: false,
    });
  }

  const configuredStateDir = options.config?.stateDir ? expandPath(options.config.stateDir) : "";
  const legacyState = expandPath(options.legacyStateDir || legacyDefaultStateDir(env));
  if (dirExists(legacyState) && !isInside(legacyState, homeDir)) {
    addStateDir({ root: legacyState, reason: "legacy relaymux state (~/.local/state/relaymux)", stateDir, logsDir, addItem, ownedDir: true });
  }
  if (configuredStateDir && dirExists(configuredStateDir) && !samePath(configuredStateDir, legacyState) && !isInside(configuredStateDir, homeDir) && looksRelaymuxStateDir(configuredStateDir)) {
    addStateDir({ root: configuredStateDir, reason: "configured relaymux stateDir outside ~/.relaymux", stateDir, logsDir, addItem, ownedDir: true });
  }

  const orchestratorImessageDir = expandPath(options.orchestratorImessageDir || path.join(os.homedir(), ".pi", "agent", "orchestrator-imessage"));
  if (dirExists(orchestratorImessageDir)
      && !isInside(orchestratorImessageDir, homeDir)
      && (!configuredStateDir || !samePath(orchestratorImessageDir, configuredStateDir))
      && !samePath(orchestratorImessageDir, legacyState)) {
    addOrchestratorImessageDir({ root: orchestratorImessageDir, stateDir, logsDir, addItem });
  }

  const agentmuxConfig = expandPath(options.agentmuxConfigPath || path.join(os.homedir(), ".config", "agentmux", "config.json"));
  if (fileExists(agentmuxConfig) && !samePath(agentmuxConfig, targetConfigPath) && isRelaymuxConfigFile(agentmuxConfig)) {
    addItem({
      operation: "migrate-config",
      kind: "file",
      source: agentmuxConfig,
      destination: hasConfigMigration(items, targetConfigPath) || fileExists(targetConfigPath)
        ? path.join(homeDir, "config.agentmux.json")
        : targetConfigPath,
      reason: "old agentmux config that matches relaymux schema",
      secret: false,
    });
  }

  const agentmuxState = expandPath(options.agentmuxStateDir || path.join(os.homedir(), ".local", "state", "agentmux"));
  if (dirExists(agentmuxState) && !isInside(agentmuxState, homeDir)) {
    addAgentmuxState({ root: agentmuxState, stateDir, logsDir, addItem });
  }

  return {
    homeDir,
    stateDir,
    logsDir,
    configPath: targetConfigPath,
    defaultConfigPath: defaultConfigPath(env),
    legacyConfigPath: legacyConfig,
    legacyStateDir: legacyState,
    items,
  };
}

export function formatHomeMigrationInventory(inventory, { applying = false, force = false, symlink = false } = {}) {
  const lines = [];
  lines.push(`relaymux home: ${inventory.homeDir}`);
  lines.push(`target config: ${inventory.configPath}`);
  lines.push(`mode: ${applying ? "apply" : "dry-run/inventory"}${force ? "; force overwrite" : ""}${symlink ? "; replace migrated sources with symlinks" : ""}`);
  lines.push("managed layout: config.json, relaymux.sqlite3, state/, logs/");

  if (!inventory.items.length) {
    lines.push("No relaymux-owned legacy files found in known locations.");
    return `${lines.join("\n")}\n`;
  }

  lines.push("Planned relaymux-owned items (contents hidden; secrets are never printed):");
  for (const item of inventory.items) {
    const destination = item.destination || "(review only; no automatic destination)";
    const secret = item.secret ? "; secret/token, copy mode forced to 0600" : "";
    const status = item.destination && pathExists(item.destination) && !force
      ? "; destination exists, apply will skip unless --force"
      : "";
    lines.push(`- ${item.operation}\t${item.kind}\t${item.source} -> ${destination}\t${item.reason}${secret}${status}`);
  }
  if (!applying) {
    lines.push("Run `relaymux migrate-home --apply` to copy these items. Add `--symlink` only if you want old relaymux-owned paths replaced by symlinks after copying.");
  }
  return `${lines.join("\n")}\n`;
}

export function applyHomeMigration(inventory, { force = false, symlink = false, env = process.env } = {}) {
  ensureRelaymuxHomeLayout(inventory.homeDir);
  const results = [];

  for (const item of inventory.items) {
    if (!item.destination) {
      results.push({ ...item, status: "skipped", detail: "review-only item" });
      continue;
    }
    if (!pathExists(item.source)) {
      results.push({ ...item, status: "missing", detail: "source disappeared" });
      continue;
    }
    const sourceStat = fs.lstatSync(item.source);
    const canMergeDirectory = sourceStat.isDirectory() && dirExists(item.destination);
    if (pathExists(item.destination) && !force && !canMergeDirectory) {
      results.push({ ...item, status: "skipped", detail: "destination exists; use --force to overwrite" });
      continue;
    }

    if (item.operation === "migrate-config") {
      writeMigratedConfig(item.source, item.destination, { force, homeDir: inventory.homeDir, env });
    } else {
      copyPath(item.source, item.destination, { force, secret: item.secret });
    }

    if (symlink && item.symlinkSafe) {
      replaceSourceWithSymlink(item.source, item.destination);
      results.push({ ...item, status: "copied+symlinked", detail: "source replaced with symlink; backup left beside source" });
    } else {
      results.push({ ...item, status: "copied", detail: "source left in place" });
    }
  }

  return results;
}

export function formatHomeMigrationResults(results) {
  if (!results.length) return "No migration actions were needed.\n";
  const lines = ["Migration results:"];
  for (const result of results) {
    lines.push(`- ${result.status}\t${result.source} -> ${result.destination || "(none)"}\t${result.detail || ""}`);
  }
  lines.push("Next: inspect ~/.relaymux/config.json, then run `relaymux doctor` and `relaymux restart-launch-agent` when ready.");
  return `${lines.join("\n")}\n`;
}

function addStateDir({ root, reason, stateDir, logsDir, addItem, ownedDir }) {
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const source = path.join(root, entry.name);
    if (!ownedDir && !STATE_RELAYMUX_NAMES.has(entry.name) && !entry.name.includes("relaymux")) continue;
    const destination = entry.name === "logs"
      ? logsDir
      : path.join(stateDir, entry.name);
    addItem({
      operation: "copy",
      kind: entry.isDirectory() ? "dir" : "file",
      source,
      destination,
      reason: `${reason}: ${entry.name}`,
      secret: isSecretPath(entry.name),
    });
  }
}

function addOrchestratorImessageDir({ root, stateDir, logsDir, addItem }) {
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const name = entry.name;
    const source = path.join(root, name);
    if (!ORCHESTRATOR_IMESSAGE_NAMES.has(name) && !name.startsWith("relaymux")) continue;

    let destination;
    if (name === "logs") destination = logsDir;
    else if (name.endsWith(".log")) destination = path.join(logsDir, name);
    else if (name === "prompts") destination = path.join(stateDir, "prompts");
    else if (name === "scripts") destination = path.join(stateDir, "scripts");
    else if (name === "sessions") destination = path.join(stateDir, "sessions");
    else if (name === "token") destination = path.join(stateDir, "orchestrator-imessage-token");
    else destination = path.join(stateDir, name);

    addItem({
      operation: "copy",
      kind: entry.isDirectory() ? "dir" : "file",
      source,
      destination,
      reason: "relaymux state/log/session files under ~/.pi/agent/orchestrator-imessage",
      secret: isSecretPath(name),
    });
  }
}

function addAgentmuxState({ root, stateDir, logsDir, addItem }) {
  const relaymuxChild = path.join(root, "relaymux");
  if (dirExists(relaymuxChild)) {
    addStateDir({ root: relaymuxChild, reason: "old agentmux relaymux state", stateDir, logsDir, addItem, ownedDir: true });
  }

  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    if (entry.name === "relaymux") continue;
    if (!entry.name.includes("relaymux") && !entry.name.includes("orchestrator-imessage")) continue;
    addItem({
      operation: "copy",
      kind: entry.isDirectory() ? "dir" : "file",
      source: path.join(root, entry.name),
      destination: path.join(stateDir, "agentmux", entry.name),
      reason: "relaymux-specific file under old agentmux state path",
      secret: isSecretPath(entry.name),
    });
  }
}

function writeMigratedConfig(source, destination, { force, homeDir, env }) {
  if (pathExists(destination) && force) fs.rmSync(destination, { recursive: true, force: true });
  ensureDirectory(path.dirname(destination));
  const parsed = JSON.parse(fs.readFileSync(source, "utf8"));
  const migrated = migrateConfigObject(parsed, { homeDir, env });
  fs.writeFileSync(destination, `${JSON.stringify(migrated, null, 2)}\n`, { mode: 0o600 });
  try { fs.chmodSync(destination, 0o600); } catch {}
}

export function migrateConfigObject(config, { homeDir = defaultRelaymuxHome(), env = process.env } = {}) {
  const homeValue = configHomeValue(homeDir, env);
  const stateValue = path.posix.join(homeValue, "state");
  const logsValue = path.posix.join(homeValue, "logs");
  const tokenValue = path.posix.join(stateValue, "webhook-token");
  const sessionsValue = path.posix.join(stateValue, "sessions");
  const migrated = deepClone(config || {});

  if (!migrated.version) migrated.version = 1;
  if (!migrated.stateDir || shouldRewriteManagedPath(migrated.stateDir)) {
    migrated.stateDir = stateValue;
  }

  migrated.daemon = migrated.daemon || {};
  if (!migrated.daemon.tokenFile || shouldRewriteManagedPath(migrated.daemon.tokenFile)) {
    migrated.daemon.tokenFile = tokenValue;
  }
  if (!migrated.daemon.logDir || shouldRewriteManagedPath(migrated.daemon.logDir)) {
    migrated.daemon.logDir = logsValue;
  }

  const command = migrated.orchestrator?.command;
  if (Array.isArray(command)) {
    migrated.orchestrator.command = command.map((part) => rewriteManagedPathString(part, {
      state: stateValue,
      logs: logsValue,
      token: tokenValue,
      sessions: sessionsValue,
    }));
  }

  return migrated;
}

function copyPath(source, destination, { force, secret }) {
  ensureDirectory(path.dirname(destination));
  const stat = fs.lstatSync(source);
  if (pathExists(destination) && force) fs.rmSync(destination, { recursive: true, force: true });
  if (stat.isDirectory()) {
    fs.cpSync(source, destination, { recursive: true, force: Boolean(force), errorOnExist: !force, preserveTimestamps: true });
    secureTokenFiles(destination);
    return;
  }
  if (stat.isSymbolicLink()) {
    fs.symlinkSync(fs.readlinkSync(source), destination);
    return;
  }
  fs.copyFileSync(source, destination);
  const mode = secret ? 0o600 : (stat.mode & 0o777);
  try { fs.chmodSync(destination, mode); } catch {}
}

function replaceSourceWithSymlink(source, destination) {
  if (!pathExists(source)) return;
  const stat = fs.lstatSync(source);
  if (stat.isSymbolicLink()) return;
  const backup = `${source}.relaymux-migrated-${new Date().toISOString().replace(/[:.]/g, "-")}`;
  fs.renameSync(source, backup);
  try {
    fs.symlinkSync(destination, source, stat.isDirectory() ? "dir" : "file");
  } catch (error) {
    fs.renameSync(backup, source);
    throw error;
  }
}

function secureTokenFiles(root) {
  const stat = fs.lstatSync(root);
  if (!stat.isDirectory()) {
    if (isSecretPath(path.basename(root))) try { fs.chmodSync(root, 0o600); } catch {}
    return;
  }
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const child = path.join(root, entry.name);
    if (entry.isDirectory()) secureTokenFiles(child);
    else if (isSecretPath(entry.name)) try { fs.chmodSync(child, 0o600); } catch {}
  }
}

function isRelaymuxConfigFile(file) {
  try {
    const parsed = JSON.parse(fs.readFileSync(file, "utf8"));
    return Boolean(parsed && typeof parsed === "object" && !Array.isArray(parsed) && (
      parsed.imessage || parsed.daemon || parsed.orchestrator || parsed.agents || parsed.stateDir
    ));
  } catch {
    return false;
  }
}

function looksRelaymuxStateDir(dir) {
  try {
    const names = fs.readdirSync(dir);
    return names.some((name) => STATE_RELAYMUX_NAMES.has(name) || name.includes("relaymux"));
  } catch {
    return false;
  }
}

function shouldRewriteManagedPath(value) {
  const text = String(value || "");
  return text.includes("/.local/state/relaymux")
    || text.includes("~/.local/state/relaymux")
    || text.includes("/.config/relaymux")
    || text.includes("~/.config/relaymux")
    || text.includes("/.local/state/agentmux")
    || text.includes("~/.local/state/agentmux")
    || text.includes("/.config/agentmux")
    || text.includes("~/.config/agentmux")
    || text.includes("/.pi/agent/orchestrator-imessage")
    || text.includes("orchestrator-imessage");
}

function rewriteManagedPathString(value, replacements) {
  if (typeof value !== "string") return value;
  if (!shouldRewriteManagedPath(value)) return value;
  let next = value;
  const home = os.homedir();
  const mappings = [
    ["~/.local/state/relaymux/sessions", replacements.sessions],
    [path.join(home, ".local", "state", "relaymux", "sessions"), replacements.sessions],
    ["~/.local/state/relaymux/webhook-token", replacements.token],
    [path.join(home, ".local", "state", "relaymux", "webhook-token"), replacements.token],
    ["~/.local/state/relaymux/logs", replacements.logs],
    [path.join(home, ".local", "state", "relaymux", "logs"), replacements.logs],
    ["~/.local/state/relaymux", replacements.state],
    [path.join(home, ".local", "state", "relaymux"), replacements.state],
    ["~/.pi/agent/orchestrator-imessage", path.posix.join(path.posix.dirname(replacements.state), "state")],
    [path.join(home, ".pi", "agent", "orchestrator-imessage"), replacements.state],
  ];
  for (const [oldValue, newValue] of mappings) {
    next = next.split(oldValue).join(newValue);
  }
  return next;
}

function configHomeValue(homeDir, env) {
  const defaultHome = defaultRelaymuxHome(env);
  if (!env.RELAYMUX_HOME && samePath(homeDir, defaultHome)) return "~/.relaymux";
  return homeDir;
}

function hasConfigMigration(items, targetConfigPath) {
  return items.some((item) => item.operation === "migrate-config" && samePath(item.destination, targetConfigPath));
}

function pathKind(value) {
  try {
    const stat = fs.lstatSync(value);
    if (stat.isDirectory()) return "dir";
    if (stat.isSymbolicLink()) return "symlink";
    return "file";
  } catch {
    return "missing";
  }
}

function isSecretPath(value) {
  return /token|secret|credential|key/i.test(String(value));
}

function fileExists(value) {
  try { return fs.statSync(expandPath(value)).isFile(); } catch { return false; }
}

function dirExists(value) {
  try { return fs.statSync(expandPath(value)).isDirectory(); } catch { return false; }
}

function pathExists(value) {
  try { fs.lstatSync(value); return true; } catch { return false; }
}

function samePath(a, b) {
  return path.resolve(expandPath(a)) === path.resolve(expandPath(b));
}

function isInside(child, parent) {
  const relative = path.relative(path.resolve(parent), path.resolve(child));
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

function deepClone(value) {
  return JSON.parse(JSON.stringify(value));
}
