import fs from "node:fs";
import path from "node:path";
import { performance } from "node:perf_hooks";

import {
  elapsedMs,
  finishLatencyFields,
  formatLatencyLogLine,
  jobType,
  selectNextJobIndex,
  stampQueuedJob,
  startJobMetrics,
  startLatencyFields,
  summarizeQueuedJobs,
  timedStage,
} from "./daemon-jobs.js";
import { createCompletionWebhookServer } from "./webhook.js";
import { buildIncomingOrchestratorParts, buildIncomingOrchestratorPrompt, buildTerminalOrchestratorParts, buildTerminalOrchestratorPrompt, buildWebhookOrchestratorParts, buildWebhookOrchestratorPrompt, joinPromptParts, runOrchestrator } from "./orchestrator.js";
import { formatIncomingForPrompt, isImessageReceiveEnabled, isIncomingUserMessage, receiveMessages, sendMessage } from "./message-io.js";
import { isTelegramReceiveEnabled, receiveTelegramMessages, sendTelegramMessage } from "./telegram.js";
import { ensureDirectory } from "./paths.js";
import { validateSessionName } from "./tmux.js";

export async function runDaemon({ flags, configInfo, stateDir, io = defaultIo() }) {
  const sessionOverride = flags.session || io.env?.RELAYMUX_SESSION;
  if (sessionOverride) {
    validateSessionName(String(sessionOverride));
  }
  const config = sessionOverride
    ? { ...configInfo.config, session: String(sessionOverride) }
    : configInfo.config;
  ensureDirectory(stateDir);
  const stateFile = path.join(stateDir, "daemon-state.json");
  const state = readDaemonState(stateFile);
  const queue = [];
  const queuedIncomingIds = new Set();
  let processing = false;
  let scheduled = false;
  let polling = false;

  const log = (...args) => io.stdout.write(`[${new Date().toISOString()}] ${args.join(" ")}\n`);
  const warn = (...args) => io.stderr.write(`[${new Date().toISOString()}] ${args.join(" ")}\n`);
  const saveState = () => writeDaemonState(stateFile, state);

  function queueStatus() {
    return {
      queueLength: queue.length,
      queueByType: summarizeQueuedJobs(queue),
      processing,
      scheduled,
      polling,
      queuedIncomingIds: queuedIncomingIds.size,
      initialized: state.initialized,
      lastProcessedAt: state.lastProcessedAt,
      lastWebhookAt: state.lastWebhookAt,
    };
  }

  function scheduleProcessQueue() {
    if (processing || scheduled) return;
    scheduled = true;
    setImmediate(() => {
      scheduled = false;
      processQueue().catch((error) => warn("queue processor failed:", describeError(error)));
    });
  }

  function enqueueIncoming(adapter, messages, replyMode) {
    if (!messages.length) return;
    const ids = messages.map((message) => String(message.id));
    for (const id of ids) queuedIncomingIds.add(id);
    queue.push(stampQueuedJob({ type: "incoming", adapter, replyMode, requestId: `${adapter}-${Date.now().toString(36)}`, messages, ids }));
    log(`queued incoming ${adapter} message(s): ${ids.join(",")}`);
    scheduleProcessQueue();
  }

  function enqueueWebhook(job) {
    queue.push(stampQueuedJob(job));
    log(`queued local completion ${job.requestId} from ${job.source}; replyMode=${job.replyMode}`);
    scheduleProcessQueue();
  }

  function enqueueTerminalRequest(job) {
    queue.push(stampQueuedJob(job));
    log(`queued terminal request ${job.requestId} from ${job.source}; replyMode=${job.replyMode}; wait=${job.wait ? "yes" : "no"}`);
    scheduleProcessQueue();
  }

  function enqueueJob(job) {
    if (job.type === "request") enqueueTerminalRequest(job);
    else enqueueWebhook(job);
  }

  async function processIncomingJob(job) {
    log(`processing incoming ${job.adapter || "message"} message(s): ${job.ids.join(",")}`);
    const metrics = startJobMetrics(job, queue.length);
    log(formatLatencyLogLine("job_start", startLatencyFields(metrics)));
    let status = "ok";
    let marked = false;
    const markSeen = () => {
      if (marked) return;
      rememberSeen(state, job.ids);
      saveState();
      marked = true;
    };

    try {
      const parts = buildIncomingOrchestratorParts({
        config,
        configPath: configInfo.path,
        incomingText: formatIncomingForPrompt(job.messages, job.adapter),
      });
      const prompt = joinPromptParts(parts);
      const reply = await timedStage(metrics, "orchestratorMs", () => runOrchestrator(config, { prompt, promptParts: parts, stateDir, configPath: configInfo.path, requestId: job.requestId }));
      await timedStage(metrics, "adapterSendMs", () => sendReply(config, job.replyMode || "imessage", reply, io));
      markSeen();
      log(`replied to ${job.ids.join(",")} via ${job.replyMode || "imessage"}`);
    } catch (error) {
      status = "error";
      warn(`failed processing incoming ${job.ids.join(",")}:`, describeError(error));
      try {
        await timedStage(metrics, "adapterSendMs", () => sendReply(config, job.replyMode || "imessage", `relaymux orchestrator hit an error: ${error.message || String(error)}`, io));
      } catch (sendError) {
        warn("also failed to send error message:", describeError(sendError));
      }
      markSeen();
    } finally {
      for (const id of job.ids) queuedIncomingIds.delete(id);
      log(formatLatencyLogLine("job_done", finishLatencyFields(metrics, status)));
    }
  }

  async function processWebhookJob(job) {
    log(`processing local completion ${job.requestId} from ${job.source}`);
    const metrics = startJobMetrics(job, queue.length);
    log(formatLatencyLogLine("job_start", startLatencyFields(metrics)));
    let status = "ok";
    try {
      const parts = buildWebhookOrchestratorParts({ config, configPath: configInfo.path, job });
      const prompt = joinPromptParts(parts);
      const reply = await timedStage(metrics, "orchestratorMs", () => runOrchestrator(config, { prompt, promptParts: parts, stateDir, configPath: configInfo.path, requestId: job.requestId }));
      if (job.replyMode === "none") {
        log(`processed quiet completion ${job.requestId}: ${oneLine(reply).slice(0, 240)}`);
      } else {
        await timedStage(metrics, "adapterSendMs", () => sendReply(config, job.replyMode, reply, io));
        log(`sent ${job.replyMode} completion reply for ${job.requestId}`);
      }
    } catch (error) {
      status = "error";
      warn(`failed processing local completion ${job.requestId}:`, describeError(error));
      if (job.replyMode !== "none") {
        try {
          await timedStage(metrics, "adapterSendMs", () => sendReply(config, job.replyMode, `relaymux orchestrator hit an error processing ${job.source}: ${error.message || String(error)}`, io));
        } catch (sendError) {
          warn("also failed to send completion error message:", describeError(sendError));
        }
      }
    } finally {
      log(formatLatencyLogLine("job_done", finishLatencyFields(metrics, status)));
    }
  }

  async function processTerminalRequestJob(job) {
    log(`processing terminal request ${job.requestId} from ${job.source}`);
    const metrics = startJobMetrics(job, queue.length);
    log(formatLatencyLogLine("job_start", startLatencyFields(metrics)));
    let status = "ok";
    try {
      const parts = buildTerminalOrchestratorParts({ config, configPath: configInfo.path, job });
      const prompt = joinPromptParts(parts);
      const reply = await timedStage(metrics, "orchestratorMs", () => runOrchestrator(config, { prompt, promptParts: parts, stateDir, configPath: configInfo.path, requestId: job.requestId }));
      if (job.replyMode === "none") {
        log(`processed terminal request ${job.requestId}: ${oneLine(reply).slice(0, 240)}`);
      } else {
        await timedStage(metrics, "adapterSendMs", () => sendReply(config, job.replyMode, reply, io));
        log(`sent ${job.replyMode} terminal request reply for ${job.requestId}`);
      }
      job.deferred?.resolve({ ok: true, queued: false, reply });
    } catch (error) {
      status = "error";
      const message = `relaymux orchestrator hit an error processing ${job.source}: ${error.message || String(error)}`;
      warn(`failed processing terminal request ${job.requestId}:`, describeError(error));
      if (job.replyMode !== "none") {
        try {
          await timedStage(metrics, "adapterSendMs", () => sendReply(config, job.replyMode, message, io));
        } catch (sendError) {
          warn("also failed to send terminal request error message:", describeError(sendError));
        }
      }
      job.deferred?.resolve({ ok: false, queued: false, error: message });
    } finally {
      log(formatLatencyLogLine("job_done", finishLatencyFields(metrics, status)));
    }
  }

  async function processQueue() {
    if (processing) return;
    processing = true;
    try {
      while (queue.length > 0) {
        const nextIndex = selectNextJobIndex(queue);
        const job = nextIndex <= 0 ? queue.shift() : queue.splice(nextIndex, 1)[0];
        if (nextIndex > 0) {
          log(formatLatencyLogLine("queue_priority", { selectedType: jobType(job), skippedJobs: nextIndex, queueLength: queue.length + 1 }));
        }
        if (job.type === "incoming" || job.type === "imessage") await processIncomingJob(job);
        else if (job.type === "webhook") await processWebhookJob(job);
        else if (job.type === "request") await processTerminalRequestJob(job);
        else warn("dropping unknown job:", JSON.stringify(job));
      }
    } finally {
      processing = false;
      if (queue.length > 0) scheduleProcessQueue();
    }
  }

  async function pollOnce() {
    if (polling) return;
    polling = true;
    try {
      if (inboundImessageEnabled) {
        const pollStarted = performance.now();
        try {
          const recent = await receiveMessages(config);
          const fresh = freshIncoming(recent, seenIncomingIds(state), queuedIncomingIds);
          logPollLatency("imessage", pollStarted, recent.length, fresh.length);
          if (fresh.length) enqueueIncoming("imessage", fresh, "imessage");
        } catch (error) {
          warn("iMessage poll failed:", describeError(error));
        }
      }
      if (inboundTelegramEnabled) {
        const pollStarted = performance.now();
        try {
          const recent = await receiveTelegramMessages(config, state, io.env || process.env);
          saveState();
          const fresh = freshIncoming(recent, seenIncomingIds(state), queuedIncomingIds);
          logPollLatency("telegram", pollStarted, recent.length, fresh.length);
          if (fresh.length) enqueueIncoming("telegram", fresh, "telegram");
        } catch (error) {
          warn("Telegram poll failed:", describeError(error));
        }
      }
    } finally {
      polling = false;
    }
  }

  function logPollLatency(adapter, startedAt, messages, fresh) {
    const durationMs = elapsedMs(startedAt);
    if (!fresh && durationMs < 1000) return;
    log(formatLatencyLogLine("poll", { adapter, durationMs, messages, fresh, queueLength: queue.length, processing }));
  }

  const inboundImessageEnabled = isImessageReceiveEnabled(config);
  const inboundTelegramEnabled = isTelegramReceiveEnabled(config);
  const inboundEnabled = inboundImessageEnabled || inboundTelegramEnabled;
  log("starting relaymux background daemon");
  if (inboundImessageEnabled) log("iMessage/SMS adapter enabled for inbound polling");
  if (inboundTelegramEnabled) log("Telegram adapter enabled for inbound polling");
  if (!inboundEnabled) log("no inbound message adapter enabled; serving local API/webhook only");

  if (inboundEnabled && !state.initialized) {
    try {
      const recent: any[] = [];
      if (inboundImessageEnabled) {
        try { recent.push(...await receiveMessages(config)); }
        catch (error) { warn("iMessage initial sync failed:", describeError(error)); }
      }
      if (inboundTelegramEnabled) {
        try { recent.push(...await receiveTelegramMessages(config, state, io.env || process.env)); }
        catch (error) { warn("Telegram initial sync failed:", describeError(error)); }
      }
      const initialIds = recent.filter(isIncomingUserMessage).map((message) => String(message.id));
      state.initialized = true;
      rememberSeen(state, initialIds);
      saveState();
      log(`initialized; marked ${initialIds.length} existing incoming message(s) as seen`);
    } catch (error) {
      warn("initial sync failed:", describeError(error));
      state.initialized = true;
      saveState();
    }
  }

  const webhookServer = config.daemon?.enabled === false
    ? null
    : await createCompletionWebhookServer({
        config,
        state,
        saveState,
        enqueue: enqueueJob,
        getStatus: queueStatus,
        io: { warn },
      });
  if (webhookServer) log(`local completion webhook listening on ${config.daemon?.host || "127.0.0.1"}:${Number(config.daemon?.port || 47761)}`);

  if (inboundEnabled) {
    await pollOnce().catch((error) => warn("initial poll failed:", describeError(error)));
  }
  await drainIfOnce(flags, processQueue);

  if (flags.once) {
    if (webhookServer) await closeServer(webhookServer);
    log("daemon --once complete");
    return 0;
  }

  const pollMs = Math.min(
    inboundImessageEnabled ? Number(config.integrations?.imessage?.pollMs || config.imessage?.pollMs || 3000) : Infinity,
    inboundTelegramEnabled ? Number(config.integrations?.telegram?.receive?.pollMs || 3000) : Infinity,
  );
  const interval = inboundEnabled
    ? setInterval(() => {
        pollOnce().catch((error) => warn("poll failed:", describeError(error)));
      }, Number.isFinite(pollMs) ? pollMs : 3000)
    : null;

  await new Promise<void>((resolve) => {
    const shutdown = async (signal) => {
      log(`shutting down (${signal})`);
      if (interval) clearInterval(interval);
      if (webhookServer) await closeServer(webhookServer);
      resolve();
    };
    process.once("SIGTERM", () => shutdown("SIGTERM"));
    process.once("SIGINT", () => shutdown("SIGINT"));
  });
  return 0;
}

export function readDaemonState(stateFile) {
  try {
    const parsed = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    return {
      initialized: Boolean(parsed.initialized),
      seenIncomingIds: Array.isArray(parsed.seenIncomingIds) ? parsed.seenIncomingIds.map(String) : [],
      seenWebhookIdempotencyKeys: Array.isArray(parsed.seenWebhookIdempotencyKeys) ? parsed.seenWebhookIdempotencyKeys.map(String) : [],
      lastTelegramUpdateId: parsed.lastTelegramUpdateId,
      lastProcessedAt: parsed.lastProcessedAt,
      lastWebhookAt: parsed.lastWebhookAt,
    };
  } catch {
    return { initialized: false, seenIncomingIds: [], seenWebhookIdempotencyKeys: [] };
  }
}

export function writeDaemonState(stateFile, state) {
  ensureDirectory(path.dirname(stateFile));
  const tmp = `${stateFile}.tmp`;
  fs.writeFileSync(tmp, `${JSON.stringify(state, null, 2)}\n`);
  fs.renameSync(tmp, stateFile);
}

function rememberSeen(state, ids) {
  const seen = new Set((state.seenIncomingIds || []).map(String));
  for (const id of ids) seen.add(String(id));
  state.seenIncomingIds = Array.from(seen).slice(-1000);
  state.lastProcessedAt = new Date().toISOString();
}

function seenIncomingIds(state) {
  return new Set((state.seenIncomingIds || []).map(String));
}

function freshIncoming(messages, seen, queuedIncomingIds) {
  return messages
    .filter(isIncomingUserMessage)
    .filter((message) => !seen.has(String(message.id)) && !queuedIncomingIds.has(String(message.id)))
    .sort(compareMessages);
}

function compareMessages(a, b) {
  const aId = Number(a.id);
  const bId = Number(b.id);
  if (Number.isFinite(aId) && Number.isFinite(bId)) return aId - bId;
  return String(a.createdAt || a.id).localeCompare(String(b.createdAt || b.id));
}

async function drainIfOnce(flags, processQueue) {
  if (!flags.once) return;
  await processQueue();
}

async function sendReply(config, replyMode, text, io) {
  if (replyMode === "imessage") return sendMessage(config, text, io);
  if (replyMode === "telegram") return sendTelegramMessage(config, text, io, io.env || process.env);
  throw new Error(`unsupported reply mode: ${replyMode}`);
}

async function closeServer(server) {
  await new Promise<void>((resolve) => server.close(() => resolve()));
}

function describeError(error) {
  const parts = [error?.message || String(error)];
  const stdoutBytes = Buffer.byteLength(String(error?.stdout || ""));
  const stderrBytes = Buffer.byteLength(String(error?.stderr || ""));
  if (stdoutBytes) parts.push(`stdout captured: ${stdoutBytes} bytes`);
  if (stderrBytes) parts.push(`stderr captured: ${stderrBytes} bytes`);
  if (error?.lastActivityAt) {
    parts.push(`last activity: ${error.lastActivityAt}${error.lastActivityReason ? ` (${error.lastActivityReason})` : ""}`);
  }
  if (error?.signal) parts.push(`signal: ${error.signal}`);
  return parts.join("\n");
}

function oneLine(text) {
  return String(text || "").replace(/\s+/g, " ").trim();
}

function defaultIo() {
  return {
    env: process.env,
    stdout: process.stdout,
    stderr: process.stderr,
  };
}
