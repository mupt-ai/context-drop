import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { quoteArgv } from "../src/command.js";
import { launchTmuxAgent } from "../src/launch/tmux.js";
import { scheduleWindowName, sanitizeScheduleName } from "../src/launch/names.js";
import { reapScheduleRuns, readRuns, readEvents, latestEventsByRun } from "../src/state.js";
import { scheduleNameFromSource } from "../src/orchestrator.js";

// Build a fake `tmux` executable on PATH that logs every invocation and returns
// a stable window target for new-window/new-session so launchTmuxAgent can run
// end-to-end without a real tmux server.
function makeFakeTmux(root: string) {
  const binDir = path.join(root, "bin");
  fs.mkdirSync(binDir);
  const logFile = path.join(root, "tmux.log");
  const windowState = path.join(root, "window.exists");
  fs.writeFileSync(
    path.join(binDir, "tmux"),
    `#!/bin/sh
{
  printf 'CALL\\n'
  for arg in "$@"; do printf 'ARG:%s\\n' "$arg"; done
  printf 'END\\n'
} >> ${JSON.stringify(logFile)}
if [ "$1" = "list-windows" ]; then
  if [ -f ${JSON.stringify(windowState)} ]; then
    printf 'agents<|relaymux|>2<|relaymux|>personal-digest-hourly<|relaymux|>0<|relaymux|>1<|relaymux|>/tmp<|relaymux|>1<|relaymux|>run-old<|relaymux|>custom<|relaymux|>/tmp<|relaymux|>personal-digest-hourly<|relaymux|>2025-01-01T00:00:00Z<|relaymux|>personal-digest-hourly\\n'
  fi
  exit 0
fi
if [ "$1" = "has-session" ]; then exit 0; fi
if [ "$1" = "new-window" ] || [ "$1" = "new-session" ]; then
  touch ${JSON.stringify(windowState)}
  printf 'agents:2.0\\n'
  exit 0
fi
if [ "$1" = "kill-window" ]; then
  rm -f ${JSON.stringify(windowState)}
  exit 0
fi
exit 0
`,
    { mode: 0o755 },
  );
  return { binDir, logFile };
}

function makeLaunchRequest({ root, stateDir, name, runId, scheduleName, reuseWindow }: {
  root: string;
  stateDir: string;
  name: string;
  runId: string;
  scheduleName?: string;
  reuseWindow?: boolean;
}) {
  let stdout = "";
  return {
    request: {
      agentConfig: { command: ["/usr/bin/true"], promptMode: "none" },
      agentName: "custom",
      attach: false,
      cliPath: "/usr/local/bin/relaymux.js",
      configPath: path.join(root, "config.json"),
      dryRun: false,
      holdOnExit: false,
      io: { stdout: { write: (chunk: string) => { stdout += String(chunk); } } },
      launchNotification: undefined,
      name,
      printCommand: false,
      prompt: "noop",
      quoteArgv,
      repo: root,
      runId,
      scheduleName,
      reuseWindow,
      session: "agents",
      sessionInfo: { session: "agents", mode: "shared", source: "config.session" },
      stateDir,
      workdir: root,
      worktreeAddArgs: undefined,
    },
    get stdout() { return stdout; },
  };
}

function withFakeTmux<T>(fn: (logFile: string) => T): T {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reuse-"));
  const { binDir, logFile } = makeFakeTmux(root);
  const originalPath = process.env.PATH;
  process.env.PATH = `${binDir}${path.delimiter}${originalPath || ""}`;
  try {
    return fn(logFile);
  } finally {
    process.env.PATH = originalPath;
  }
}

test("scheduleWindowName maps a schedule name to a stable tmux-safe window name", () => {
  assert.equal(scheduleWindowName("personal-digest-hourly"), "personal-digest-hourly");
  // dots are collapsed to dashes so they cannot be parsed as pane separators;
  // when sanitization changes the name, a short hash keeps distinct schedules
  // from collapsing onto the same window.
  assert.equal(scheduleWindowName("nightly.build"), "nightly-build-e2ee6b");
  assert.equal(scheduleWindowName("  "), "schedule");
});

test("sanitizeScheduleName validates schedule identifiers", () => {
  assert.equal(sanitizeScheduleName("daily-check"), "daily-check");
  assert.throws(() => sanitizeScheduleName("daily check"), /Invalid schedule name/);
  assert.throws(() => sanitizeScheduleName(""), /Missing schedule name/);
});

test("scheduleNameFromSource extracts schedule names from schedule: sources", () => {
  assert.equal(scheduleNameFromSource("schedule:personal-digest-hourly"), "personal-digest-hourly");
  assert.equal(scheduleNameFromSource("terminal"), "");
  assert.equal(scheduleNameFromSource("schedule:"), "");
  assert.equal(scheduleNameFromSource("schedule:bad name"), "");
});

test("scheduled launch reuses one persistent window name instead of the per-tick name", () => {
  withFakeTmux((logFile) => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reuse-run-"));
    const stateDir = path.join(root, "state");
    const harness = makeLaunchRequest({
      root,
      stateDir,
      name: "personal-digest-20250115-0800",
      runId: "run-tick-1",
      scheduleName: "personal-digest-hourly",
    });

    launchTmuxAgent(harness.request);

    const calls = fs.readFileSync(logFile, "utf8");
    // The created window must be named after the schedule, not the per-tick name.
    assert.match(calls, /ARG:new-window/);
    assert.match(calls, /ARG:-n\nARG:personal-digest-hourly/);
    assert.doesNotMatch(calls, /ARG:personal-digest-20250115-0800/);
    // The first tick has no prior window to kill.
    assert.doesNotMatch(calls, /ARG:kill-window/);
    assert.match(harness.stdout, /Reused schedule personal-digest-hourly window personal-digest-hourly/);
  });
});

test("two consecutive schedule ticks leave exactly one kill + one create, and the second reaps the first", () => {
  withFakeTmux((logFile) => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reuse-two-"));
    const stateDir = path.join(root, "state");

    const tick1 = makeLaunchRequest({
      root,
      stateDir,
      name: "personal-digest-20250115-0800",
      runId: "run-tick-1",
      scheduleName: "personal-digest-hourly",
    });
    launchTmuxAgent(tick1.request);

    const tick2 = makeLaunchRequest({
      root,
      stateDir,
      name: "personal-digest-20250115-0900",
      runId: "run-tick-2",
      scheduleName: "personal-digest-hourly",
    });
    launchTmuxAgent(tick2.request);

    const calls = fs.readFileSync(logFile, "utf8");
    // Exactly one kill-window (the second tick superseding the first) and two
    // new-window calls (one per tick), both targeting the stable schedule name.
    const killCount = (calls.match(/ARG:kill-window\nARG:-t\nARG:agents:2/g) || []).length;
    const createCount = (calls.match(/ARG:new-window/g) || []).length;
    assert.equal(killCount, 1);
    assert.equal(createCount, 2);

    // Run records: two runs, both keyed to the schedule, stable name.
    const runs = readRuns(stateDir);
    assert.equal(runs.length, 2);
    assert.equal(runs[0].scheduleName, "personal-digest-hourly");
    assert.equal(runs[0].name, "personal-digest-hourly");
    assert.equal(runs[1].scheduleName, "personal-digest-hourly");
    assert.equal(runs[1].name, "personal-digest-hourly");

    // The first run is reaped by the second tick; the current run is not.
    const latest = latestEventsByRun(stateDir);
    assert.equal(latest.get("run-tick-1")?.event, "reaped");
    assert.equal(latest.get("run-tick-2"), undefined);
  });
});

test("one-off launches without a schedule name keep unique-tab behavior and never kill or reap", () => {
  withFakeTmux((logFile) => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reuse-oneoff-"));
    const stateDir = path.join(root, "state");
    const harness = makeLaunchRequest({
      root,
      stateDir,
      name: "ad-hoc-task",
      runId: "run-adhoc",
    });

    launchTmuxAgent(harness.request);

    const calls = fs.readFileSync(logFile, "utf8");
    assert.match(calls, /ARG:new-window/);
    assert.match(calls, /ARG:-n\nARG:ad-hoc-task/);
    assert.doesNotMatch(calls, /ARG:kill-window/);
    assert.doesNotMatch(harness.stdout, /Reused schedule/);

    const runs = readRuns(stateDir);
    assert.equal(runs.length, 1);
    assert.equal(runs[0].scheduleName, undefined);
    assert.equal(runs[0].name, "ad-hoc-task");
  });
});

test("--no-reuse-window opts out of persistent reuse even with a schedule name", () => {
  withFakeTmux((logFile) => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reuse-optout-"));
    const stateDir = path.join(root, "state");
    const harness = makeLaunchRequest({
      root,
      stateDir,
      name: "personal-digest-20250115-0800",
      runId: "run-optout",
      scheduleName: "personal-digest-hourly",
      reuseWindow: false,
    });

    launchTmuxAgent(harness.request);

    const calls = fs.readFileSync(logFile, "utf8");
    assert.match(calls, /ARG:-n\nARG:personal-digest-20250115-0800/);
    assert.doesNotMatch(calls, /ARG:kill-window/);
    assert.doesNotMatch(harness.stdout, /Reused schedule/);
    const runs = readRuns(stateDir);
    assert.equal(runs[0].scheduleName, undefined);
  });
});

test("reapScheduleRuns only reaps prior runs of the same schedule and skips terminal ones", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reap-"));
  const stateDir = path.join(root, "state");
  fs.mkdirSync(stateDir, { recursive: true });

  // Two prior runs for the same schedule, one already completed.
  const runs = [
    { runId: "r1", scheduleName: "daily", name: "daily", time: "2025-01-01T08:00:00Z" },
    { runId: "r2", scheduleName: "daily", name: "daily", time: "2025-01-01T09:00:00Z" },
    { runId: "r3", scheduleName: "other", name: "other", time: "2025-01-01T09:00:00Z" },
  ];
  for (const run of runs) {
    fs.appendFileSync(path.join(stateDir, "runs.jsonl"), `${JSON.stringify(run)}\n`);
  }
  // r1 already has a completed event.
  fs.appendFileSync(path.join(stateDir, "events.jsonl"), `${JSON.stringify({ runId: "r1", event: "completed", time: "2025-01-01T08:05:00Z" })}\n`);

  const reaped = reapScheduleRuns(stateDir, "daily", { exceptRunId: "r-current" });

  // Only r2 is reaped (r1 already completed, r3 is a different schedule).
  assert.equal(reaped.length, 1);
  assert.equal(reaped[0].runId, "r2");
  assert.equal(reaped[0].event, "reaped");

  const latest = latestEventsByRun(stateDir);
  assert.equal(latest.get("r1")?.event, "completed");
  assert.equal(latest.get("r2")?.event, "reaped");
  assert.equal(latest.get("r3"), undefined);
});

test("scheduled launch dry-run reports the persistent window without touching tmux", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-reuse-dry-"));
  const stateDir = path.join(root, "state");
  const binDir = path.join(root, "bin");
  fs.mkdirSync(binDir);
  const tmuxPath = path.join(binDir, "tmux");
  fs.writeFileSync(tmuxPath, `#!/bin/sh\necho "should-not-run" >> ${JSON.stringify(path.join(root, "tmux.log"))}\nexit 0\n`, { mode: 0o755 });
  const originalPath = process.env.PATH;
  process.env.PATH = `${binDir}${path.delimiter}${originalPath || ""}`;
  try {
    let stdout = "";
    launchTmuxAgent({
      agentConfig: { command: ["/usr/bin/true"], promptMode: "none" },
      agentName: "custom",
      attach: false,
      cliPath: "/usr/local/bin/relaymux.js",
      configPath: path.join(root, "config.json"),
      dryRun: true,
      holdOnExit: false,
      io: { stdout: { write: (chunk: string) => { stdout += String(chunk); } } },
      launchNotification: undefined,
      name: "personal-digest-20250115-0800",
      printCommand: false,
      prompt: "noop",
      quoteArgv,
      repo: root,
      runId: "run-dry",
      scheduleName: "personal-digest-hourly",
      reuseWindow: true,
      session: "agents",
      sessionInfo: { session: "agents", mode: "shared", source: "config.session" },
      stateDir,
      workdir: root,
      worktreeAddArgs: undefined,
    } as any);

    assert.match(stdout, /# schedule: personal-digest-hourly \(reusing persistent window personal-digest-hourly\)/);
    assert.equal(fs.existsSync(path.join(root, "tmux.log")), false);
  } finally {
    process.env.PATH = originalPath;
  }
});
