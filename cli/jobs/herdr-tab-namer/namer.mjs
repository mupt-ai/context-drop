#!/usr/bin/env node
// Herdr tab auto-namer.
// Reads every tab across all workspaces, asks a local Ollama model for a
// short label (or KEEP), and renames tabs whose content has changed.
//
// Env:
//   HERDR_ENV=1
//   HERDR_SOCKET_PATH=/Users/avyay/.config/herdr/herdr.sock
//   OLLAMA_HOST=127.0.0.1:11435
//   OLLAMA_MODEL=qwen3.8:27b-mlx
//
// Usage: node namer.mjs [--dry-run] [--once]

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { homedir } from 'node:os';
import { setInterval as setIntervalFn, setTimeout } from 'node:timers';

const DRY = process.argv.includes('--dry-run');
const ONCE = process.argv.includes('--once') || !process.env.NAMER_INTERVAL;
const INTERVAL_MS = 30 * 60 * 1000;
const OLLAMA_HOST = process.env.OLLAMA_HOST || '127.0.0.1:11435';
const OLLAMA_MODEL = process.env.OLLAMA_MODEL || 'qwen3.8:27b-mlx';
const READ_LINES = 40;
const MAX_LABEL_LEN = 30;
const MAX_WORDS = 5;
const MIN_WORDS = 1;

const LOG_DIR = dirname(new URL(import.meta.url).pathname);
const LOG_PATH = join(LOG_DIR, 'namer.log');
const STATE_PATH = join(LOG_DIR, 'namer-state.json');

// launchd puts Homebrew first on PATH, which may contain an older client.
const userHerdr = join(homedir(), '.local', 'bin', 'herdr');
const HERDR_BIN = process.env.HERDR_BIN || (existsSync(userHerdr) ? userHerdr : 'herdr');
const herdrEnv = {
  ...process.env,
  HERDR_ENV: '1',
  HERDR_SOCKET_PATH: process.env.HERDR_SOCKET_PATH || '/Users/avyay/.config/herdr/herdr.sock',
};

function herdrOutput(args) {
  return execFileSync(HERDR_BIN, args, {
    encoding: 'utf8',
    env: herdrEnv,
    timeout: 15000,
    maxBuffer: 8 * 1024 * 1024,
  });
}

function herdr(...args) {
  const response = JSON.parse(herdrOutput(args));
  if (response.error) throw new Error(`herdr ${args.join(' ')} failed: ${JSON.stringify(response.error)}`);
  return response;
}

function herdrText(...args) {
  try {
    return herdrOutput(args);
  } catch (e) {
    log(`herdr ${args.join(' ')} failed: ${e.message}`);
    return '';
  }
}

function log(msg) {
  const ts = new Date().toISOString();
  const line = `[${ts}] ${msg}`;
  console.error(line);
  try {
    writeFileSync(LOG_PATH, line + '\n', { flag: 'a' });
  } catch {}
}

function loadState() {
  try {
    return JSON.parse(readFileSync(STATE_PATH, 'utf8'));
  } catch {
    return {};
  }
}

function saveState(state) {
  try {
    writeFileSync(STATE_PATH, JSON.stringify(state, null, 2));
  } catch (e) {
    log(`saveState failed: ${e.message}`);
  }
}

function askOllama(prompt) {
  const body = JSON.stringify({
    model: OLLAMA_MODEL,
    stream: false,
    think: false,
    options: { temperature: 0.2, num_predict: 25 },
    messages: [
      {
        role: 'system',
        content:
          'You name terminal tabs for a coding agent multiplexer (Herdr). ' +
          'Given the current tab label, agent status, working directory, and recent terminal output, ' +
          'reply with EITHER:\n' +
          '- the single word KEEP (if the current label already describes the work well), or\n' +
          '- a concise 1-5 word label that describes what the tab is doing.\n' +
          'Rules: lowercase only, no punctuation, no quotes, no Markdown, no explanation, no trailing period. ' +
          'Output only the label or KEEP on a single line.',
      },
      { role: 'user', content: prompt },
    ],
  });

  try {
    const resp = execFileSync(
      'curl',
      [
        '-sS',
        '--max-time',
        '120',
        `http://${OLLAMA_HOST}/api/chat`,
        '-H',
        'Content-Type: application/json',
        '-d',
        body,
      ],
      { encoding: 'utf8', timeout: 130000, maxBuffer: 4 * 1024 * 1024 },
    );
    const parsed = JSON.parse(resp);
    return (parsed.message?.content || '').trim();
  } catch (e) {
    log(`ollama request failed: ${e.message}`);
    return '';
  }
}

function parseLabel(raw) {
  if (!raw) return null;
  // Take the first non-empty line.
  const firstLine = raw
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean)[0];
  if (!firstLine) return null;

  // KEEP handling: if the response starts with "keep" (case-insensitive) as a
  // standalone word, treat it as keep.
  if (/^keep\b/i.test(firstLine)) return 'KEEP';

  // Strip surrounding quotes and stray punctuation.
  let label = firstLine
    .replace(/^["'`]+|["'`]+$/g, '')
    .replace(/[.!?,:;]+$/g, '')
    .trim();

  // Enforce lowercase, allow alphanumeric, spaces, dashes.
  if (!/^[a-z0-9][a-z0-9 -]*[a-z0-9]$/.test(label) && !/^[a-z0-9]$/.test(label)) return null;

  const words = label.split(/\s+/).filter(Boolean);
  if (words.length < MIN_WORDS || words.length > MAX_WORDS) return null;
  if (label.length > MAX_LABEL_LEN) return null;

  return label;
}

function snapshotTabs() {
  const wl = herdr('workspace', 'list');
  const workspaces = wl?.result?.workspaces;
  if (!Array.isArray(workspaces)) throw new Error('herdr workspace list returned no workspace array');
  const tabs = [];
  for (const ws of workspaces) {
    const tl = herdr('tab', 'list', '--workspace', ws.workspace_id);
    const pl = herdr('pane', 'list', '--workspace', ws.workspace_id);
    const tabList = tl?.result?.tabs;
    const workspacePanes = pl?.result?.panes;
    if (!Array.isArray(tabList) || !Array.isArray(workspacePanes)) {
      throw new Error(`herdr tab/pane discovery returned invalid data for ${ws.workspace_id}`);
    }
    for (const tab of tabList) {
      const panes = workspacePanes.filter((p) => p.tab_id === tab.tab_id);
      // Gather signals from the first pane in the tab.
      let recentOutput = '';
      let paneInfo = null;
      if (panes.length > 0) {
        paneInfo = panes[0];
        recentOutput = herdrText(
          'pane', 'read', paneInfo.pane_id,
          '--source', 'recent-unwrapped',
          '--lines', String(READ_LINES),
          '--format', 'text',
        );
      }
      tabs.push({
        tab_id: tab.tab_id,
        workspace_id: ws.workspace_id,
        workspace_label: ws.label,
        current_label: tab.label,
        agent: paneInfo?.agent || null,
        agent_status: paneInfo?.agent_status || tab.agent_status,
        cwd: paneInfo?.cwd || null,
        terminal_title: paneInfo?.terminal_title_stripped || paneInfo?.terminal_title || null,
        recent_output: recentOutput.slice(-1500),
      });
    }
  }
  return tabs;
}

function buildPrompt(tab) {
  const lines = [];
  lines.push(`Current label: ${tab.current_label}`);
  if (tab.agent) lines.push(`Agent: ${tab.agent} (${tab.agent_status})`);
  else lines.push(`Agent: none (${tab.agent_status})`);
  if (tab.cwd) lines.push(`Dir: ${tab.cwd}`);
  if (tab.terminal_title) lines.push(`Terminal: ${tab.terminal_title}`);
  lines.push('');
  lines.push('Recent output (last ~40 lines, may be truncated):');
  const output = tab.recent_output.trim();
  lines.push(output || '(no output)');
  lines.push('');
  lines.push('Reply with KEEP or a 1-5 word label.');
  return lines.join('\n');
}

function runPass() {
  log(`pass start${DRY ? ' (dry-run)' : ''}`);
  const tabs = snapshotTabs();
  log(`found ${tabs.length} tabs across workspaces`);
  const state = loadState();
  let renamed = 0;
  let kept = 0;
  let skipped = 0;

  for (const tab of tabs) {
    // Skip tabs with no readable output and no agent — nothing to name.
    if (!tab.recent_output.trim() && !tab.agent) {
      skipped++;
      continue;
    }

    const prompt = buildPrompt(tab);
    const raw = askOllama(prompt);
    const parsed = parseLabel(raw);

    if (!parsed) {
      skipped++;
      log(`skip (invalid response) tab=${tab.tab_id} label="${tab.current_label}" raw="${raw.slice(0, 60)}"`);
      continue;
    }

    if (parsed === 'KEEP') {
      kept++;
      log(`keep tab=${tab.tab_id} label="${tab.current_label}"`);
      continue;
    }

    // Skip if the model returned the same label we already have.
    if (parsed === tab.current_label) {
      kept++;
      continue;
    }

    log(`rename tab=${tab.tab_id} "${tab.current_label}" -> "${parsed}"${DRY ? ' (dry-run, not applied)' : ''}`);
    if (!DRY) {
      herdr('tab', 'rename', tab.tab_id, parsed);
      renamed++;
    } else {
      renamed++;
    }

    // Record the new label in state so we can detect churn.
    state[tab.tab_id] = { label: parsed, ts: new Date().toISOString() };
  }

  if (!DRY) saveState(state);
  log(`pass done: renamed=${renamed} kept=${kept} skipped=${skipped}`);
}

function main() {
  if (ONCE) {
    runPass();
    return;
  }
  log(`starting interval mode every ${INTERVAL_MS / 60000}m`);
  runPass();
  setIntervalFn(() => {
    try {
      runPass();
    } catch (e) {
      log(`pass crashed: ${e.message}`);
    }
  }, INTERVAL_MS);
}

try {
  main();
} catch (e) {
  log(`pass failed: ${e.message}`);
  process.exitCode = 1;
}
