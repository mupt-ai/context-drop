import fs from "node:fs";

import { recordEvent } from "./state.js";
import { renderTemplate } from "./command.js";
import { runCommand } from "./process.js";
import { killCurrentWindow } from "./tmux.js";
import { formatHostForUrl, webhookConfig } from "./webhook.js";
import { expandPath } from "./paths.js";
import { isReplyMode, replyModesText } from "./reply-modes.js";

export async function handleNotify({ flags, positionals, config, stateDir, io, tmux = defaultNotifyTmux(io) }) {
  await performNotify({ flags, positionals, config, stateDir, io });

  if (flags.suicide) {
    await suicideCurrentTmuxWindow({ io, tmux });
  }
}

async function performNotify({ flags, positionals, config, stateDir, io }) {
  const runId = flags.runId || process.env.RELAYMUX_RUN_ID;
  const message = flags.message || flags.text || positionals.join(" ");
  const replyMode = flags.replyMode;

  if (!runId && !replyMode) {
    throw new Error("Missing --run-id (for local event recording) or --reply-mode (for daemon completion webhook)");
  }

  const event = {
    time: new Date().toISOString(),
    runId,
    event: flags.event || "message",
    exitCode: flags.exitCode === undefined ? undefined : Number(flags.exitCode),
    message,
    agent: flags.agent,
    name: flags.name,
    repo: flags.repo,
    from: flags.from || flags.source,
    replyMode,
    idempotencyKey: flags.idempotencyKey,
  };

  if (runId) {
    recordEvent(stateDir, event);
    await dispatchNotifiers(config, event, io);
  }

  if (replyMode && flags.webhook !== false) {
    if (config.daemon?.enabled === false) {
      io.stdout.write(`${JSON.stringify({ ok: true, webhook: false, event })}\n`);
    } else {
      const response = await postCompletionWebhook(config, flags, event);
      io.stdout.write(`${JSON.stringify(response)}\n`);
    }
    return;
  }

  io.stdout.write(`${JSON.stringify(event)}\n`);
}

function defaultNotifyTmux(io) {
  return {
    killCurrentWindow: () => killCurrentWindow({ env: io.env || process.env }),
  };
}

async function suicideCurrentTmuxWindow({ io, tmux }) {
  await flushOutput(io);

  try {
    const result = await tmux.killCurrentWindow();
    if (!result?.killed) {
      const detail = result?.target
        ? `failed to kill current tmux window ${result.target}: ${result.error || "unknown error"}`
        : `could not resolve the current tmux window: ${result?.error || "unknown error"}`;
      io.stderr.write(`relaymux notify: --suicide requested but ${detail}; leaving window open\n`);
    }
  } catch (error) {
    io.stderr.write(`relaymux notify: --suicide requested but current tmux window cleanup failed: ${error.message}; leaving window open\n`);
  }
}

async function flushOutput(io) {
  await Promise.all([
    flushWritable(io.stdout),
    flushWritable(io.stderr),
  ]);
}

function flushWritable(stream) {
  if (!stream || typeof stream.write !== "function" || stream.write.length < 2) {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    stream.write("", resolve);
  });
}

export async function dispatchNotifiers(config, event, io) {
  const command = config.notifier?.command;
  if (command?.enabled) {
    if (!Array.isArray(command.argv) || command.argv.length === 0) {
      io.stderr.write("relaymux notify: command notifier enabled but argv is empty\n");
    } else {
      const argv = command.argv.map((part) => renderTemplate(part, eventTemplateContext(event)));
      const result = runCommand(argv[0], argv.slice(1), { allowFailure: true });
      if (result.status !== 0) {
        io.stderr.write(`relaymux notify: command notifier failed with ${result.status}\n`);
      }
    }
  }

  const webhook = config.notifier?.webhook;
  if (webhook?.enabled) {
    if (!webhook.url) {
      io.stderr.write("relaymux notify: webhook notifier enabled but url is empty\n");
      return;
    }
    try {
      const response = await fetch(webhook.url, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          ...(webhook.headers || {}),
        },
        body: JSON.stringify(event),
      });
      if (!response.ok) {
        io.stderr.write(`relaymux notify: webhook returned ${response.status}\n`);
      }
    } catch (error) {
      io.stderr.write(`relaymux notify: webhook failed: ${error.message}\n`);
    }
  }
}

async function postCompletionWebhook(config, flags, event) {
  if (!event.message?.trim()) {
    throw new Error("Missing --message/--text for daemon completion webhook");
  }
  if (!isReplyMode(event.replyMode)) {
    throw new Error(`--reply-mode must be ${replyModesText()}`);
  }

  const resolved = webhookConfig(config);
  const host = flags.host || resolved.host;
  const port = Number(flags.port || resolved.port);
  const tokenFile = expandPath(flags.tokenFile || resolved.tokenFile);
  const token = fs.readFileSync(tokenFile, "utf8").trim();
  if (!token) throw new Error(`token file is empty: ${tokenFile}`);

  let metadata: Record<string, any> = {};
  if (flags.metadataJson) {
    metadata = JSON.parse(flags.metadataJson);
    if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) {
      throw new Error("--metadata-json must be a JSON object");
    }
  }
  if (event.runId) metadata.runId = event.runId;
  if (event.event) metadata.event = event.event;
  if (event.agent) metadata.agent = event.agent;
  if (event.name) metadata.name = event.name;
  if (event.repo) metadata.repo = event.repo;

  const body = JSON.stringify({
    from: event.from || event.agent || event.name || "local-subagent",
    text: event.message,
    replyMode: event.replyMode,
    idempotencyKey: event.idempotencyKey,
    metadata,
  });
  const url = `http://${formatHostForUrl(host)}:${port}/message`;
  const response = await fetch(url, {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
    },
    body,
  });
  const text = await response.text();
  let payload;
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    payload = { raw: text };
  }
  if (!response.ok) {
    throw new Error(`completion webhook returned ${response.status}: ${text}`);
  }
  return payload;
}

function eventTemplateContext(event) {
  return {
    event: event.event || "",
    exitCode: event.exitCode ?? "",
    message: event.message || "",
    runId: event.runId || "",
    agent: event.agent || "",
    name: event.name || "",
    repo: event.repo || "",
    time: event.time || "",
  };
}
