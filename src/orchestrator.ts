import fs from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";

import { buildAgentInvocation } from "./command.js";
import { DEFAULT_ORCHESTRATOR_SYSTEM_PROMPT, buildRuntimePromptContext } from "./prompt.js";
import { resolveStateDir, resolveTokenFile } from "./config.js";
import { defaultRelaymuxHome, expandPath, ensureDirectory, pathExists, readTextFile } from "./paths.js";
import { runCommandAsync } from "./async-process.js";

export function buildIncomingOrchestratorPrompt({ config, configPath, incomingText }) {
  return joinPromptParts(
    buildIncomingOrchestratorParts({ config, configPath, incomingText }),
  );
}

export function buildIncomingOrchestratorParts({ config, configPath, incomingText }) {
  return buildPromptParts({
    config,
    configPath,
    title: "Incoming iMessage/SMS adapter turn",
    body: incomingText,
  });
}

export function buildWebhookOrchestratorPrompt({ config, configPath, job }) {
  return joinPromptParts(buildWebhookOrchestratorParts({ config, configPath, job }));
}

export function buildWebhookOrchestratorParts({ config, configPath, job }) {
  const metadataText = Object.keys(job.metadata || {}).length
    ? `\nMetadata JSON:\n${JSON.stringify(job.metadata, null, 2)}`
    : "";
  const idempotencyText = job.idempotencyKey ? `\nIdempotency key: ${job.idempotencyKey}` : "";
  const replyInstruction = job.replyMode === "none"
    ? "Reply mode is none: process this local update as context only. The daemon will not send a user-visible adapter message, so return a short internal acknowledgement."
    : `Reply mode is ${job.replyMode}: produce one concise user-visible adapter update. Avoid spam and mention only meaningful completion, failure, or blockers.`;

  return buildPromptParts({
    config,
    configPath,
    title: "Local subagent completion/update",
    body: `Source/from: ${job.source}\nRequest id: ${job.requestId}\nReceived at: ${job.receivedAt}\nReply mode: ${job.replyMode}${idempotencyText}${metadataText}\n\nMessage:\n${job.text}\n\n${replyInstruction}`,
  });
}

export function buildTerminalOrchestratorPrompt({ config, configPath, job }) {
  return joinPromptParts(buildTerminalOrchestratorParts({ config, configPath, job }));
}

export function buildTerminalOrchestratorParts({ config, configPath, job }) {
  const metadataText = Object.keys(job.metadata || {}).length
    ? `\nMetadata JSON:\n${JSON.stringify(job.metadata, null, 2)}`
    : "";
  const replyInstruction = job.replyMode === "none"
    ? "Reply mode is none: do the requested work and return one concise terminal-visible status. The daemon will not send an adapter message."
    : `Reply mode is ${job.replyMode}: do the requested work, then produce one concise user-visible adapter status. The daemon will also return the same text to the terminal command.`;

  return buildPromptParts({
    config,
    configPath,
    title: "Terminal request",
    body: `Source/from: ${job.source}\nRequest id: ${job.requestId}\nReceived at: ${job.receivedAt}\nReply mode: ${job.replyMode}${metadataText}\n\nMessage:\n${job.text}\n\nThis request came from a local terminal command. ${replyInstruction}`,
  });
}

export function buildPromptParts({ config, configPath, title, body }) {
  const daemon = config.daemon || {};
  const homeDir = resolveRelaymuxHome(config);
  const system = [
    config.orchestrator?.defaultSystemPrompt === false ? "" : DEFAULT_ORCHESTRATOR_SYSTEM_PROMPT,
    readInstructionsPromptFile(config.orchestrator?.systemPromptFile, homeDir),
    readPersonalityPromptFile(config.orchestrator?.personalityPromptFile, homeDir),
    config.orchestrator?.extraSystemPrompt,
  ].filter((part) => String(part || "").trim()).join("\n\n");
  const host = formatHostForUrl(daemon.host || "127.0.0.1");
  const webhookUrl = `http://${host}:${Number(daemon.port || 47761)}/message`;
  const runtime = buildRuntimePromptContext({
    configPath,
    session: config.session || "agents",
    sessionMode: config.tmux?.sessionMode || "shared",
    homeDir,
    stateDir: resolveStateDir(config),
    tokenFile: resolveTokenFile(config),
    webhookUrl,
  });

  const systemPrompt = [system, runtime].filter((part) => String(part || "").trim()).join("\n\n");
  const userPrompt = `# ${title}\n\n${body}`;
  return { systemPrompt, userPrompt, full: [systemPrompt, userPrompt].filter((part) => String(part || "").trim()).join("\n\n") };
}

export function buildFullPrompt({ config, configPath, title, body }) {
  return joinPromptParts(buildPromptParts({ config, configPath, title, body }));
}

export function joinPromptParts({ full }) {
  return full;
}

export async function runOrchestrator(config, { prompt, promptParts, stateDir, configPath, requestId }) {
  const orchestrator = config.orchestrator || {};
  const parts = promptParts
    ? { systemPrompt: promptParts.systemPrompt || "", userPrompt: promptParts.userPrompt || prompt }
    : { systemPrompt: "", userPrompt: prompt };
  const id = requestId || makeRequestId();
  const promptFile = writeOrchestratorPrompt(stateDir, id, parts.userPrompt, "");
  const systemPromptFile = parts.systemPrompt
    ? writeOrchestratorPrompt(stateDir, id, parts.systemPrompt, ".system")
    : "";
  const command = injectSystemPromptIntoCommand(orchestrator.command, systemPromptFile);
  const cwd = expandPath(orchestrator.cwd || "~");
  const invocation = buildAgentInvocation("orchestrator", { ...orchestrator, command }, {
    prompt: parts.userPrompt,
    promptFile,
    systemPrompt: parts.systemPrompt,
    systemPromptFile,
    configPath,
    session: config.session || "agents",
    tokenFile: resolveTokenFile(config),
    webhookPort: Number(config.daemon?.port || 47761),
    webhookHost: config.daemon?.host || "127.0.0.1",
    repo: cwd,
    workdir: cwd,
    name: "orchestrator",
    runId: id,
  });

  const input = invocation.stdinFile ? fs.readFileSync(invocation.stdinFile, "utf8") : undefined;
  const env: any = {
    ...process.env,
    ...invocation.env,
    RELAYMUX_CONFIG: configPath,
    RELAYMUX_ORCHESTRATOR: "1",
  };
  if (config.tmux?.sessionMode === "shared") {
    env.RELAYMUX_SESSION = config.session || "agents";
  }

  const result = await runCommandAsync(invocation.argv[0], invocation.argv.slice(1), {
    cwd,
    env,
    input,
    timeoutMs: Number(orchestrator.timeoutMs || 0),
    timeoutMode: orchestrator.timeoutMode || "activity",
    hardTimeoutMs: Number(orchestrator.hardTimeoutMs || 0),
    activityCheckIntervalMs: Number(orchestrator.activityCheckIntervalMs || 0),
    activityPaths: resolveOrchestratorActivityPaths(invocation.argv),
    maxBuffer: Number(orchestrator.maxBufferBytes || 10 * 1024 * 1024),
  });

  return result.stdout.trim() || result.stderr.trim() || "Done.";
}

function injectSystemPromptIntoCommand(command, systemPromptFile) {
  if (!systemPromptFile || !Array.isArray(command)) return command;
  const alreadyHasSystemPrompt = command.some(
    (part) => String(part).includes("{systemPrompt}") || String(part).includes("{systemPromptFile}"),
  );
  if (alreadyHasSystemPrompt) return command;
  const promptIndex = command.findIndex((part) => String(part).includes("{prompt}"));
  if (promptIndex === -1) return command;
  const modified = [...command];
  modified.splice(promptIndex, 0, "--system-prompt", "{systemPromptFile}");
  return modified;
}

function writeOrchestratorPrompt(stateDir, requestId, prompt, suffix) {
  const dir = path.join(stateDir, "prompts");
  ensureDirectory(dir);
  const file = path.join(dir, `${sanitizeFilePart(requestId)}.orchestrator${suffix}.txt`);
  fs.writeFileSync(file, prompt);
  return file;
}

function readInstructionsPromptFile(file, homeDir) {
  if (file) return readRequiredPromptFile(file);
  return readExistingPromptFile(path.join(homeDir, "AGENTS.md"));
}

function readPersonalityPromptFile(file, homeDir) {
  if (file) return readRequiredPromptFile(file);
  return readExistingPromptFile(path.join(homeDir, "SOUL.md"));
}

function readRequiredPromptFile(file) {
  if (!file) return "";
  return readTextFile(expandPath(file));
}

function readExistingPromptFile(file) {
  return pathExists(file) ? readTextFile(file) : "";
}

function resolveRelaymuxHome(config) {
  const stateDir = resolveStateDir(config);
  if (path.basename(stateDir) === "state") {
    return path.dirname(stateDir);
  }
  return defaultRelaymuxHome();
}

function sanitizeFilePart(value) {
  return String(value || "prompt").replace(/[^A-Za-z0-9_.-]+/g, "-").slice(0, 120) || "prompt";
}

function makeRequestId() {
  return `orch-${Date.now().toString(36)}-${randomUUID().slice(0, 8)}`;
}

function resolveOrchestratorActivityPaths(argv) {
  const paths = [];
  for (let index = 0; index < argv.length - 1; index += 1) {
    const value = String(argv[index + 1] || "");
    if (argv[index] === "--session" && (value.includes("/") || value.endsWith(".jsonl"))) {
      paths.push(expandPath(value));
    }
    if (argv[index] === "--session-dir") {
      const latest = latestJsonlFile(expandPath(value));
      if (latest) paths.push(latest);
    }
  }
  return Array.from(new Set(paths));
}

function latestJsonlFile(dir) {
  try {
    let latest = null;
    for (const entry of fs.readdirSync(dir)) {
      if (!entry.endsWith(".jsonl")) continue;
      const file = path.join(dir, entry);
      const stat = fs.statSync(file);
      if (!stat.isFile()) continue;
      if (!latest || stat.mtimeMs > latest.mtimeMs) latest = { file, mtimeMs: stat.mtimeMs };
    }
    return latest?.file || null;
  } catch {
    return null;
  }
}

function formatHostForUrl(host) {
  const value = String(host || "").trim();
  return value.includes(":") && !value.startsWith("[") ? `[${value}]` : value;
}
