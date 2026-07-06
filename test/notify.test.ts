import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { handleNotify } from "../src/notify.js";

function makeIo(env: Record<string, string> = {}) {
  let stdout = "";
  let stderr = "";
  return {
    io: {
      env: { ...process.env, ...env },
      stdout: { write: (chunk) => { stdout += String(chunk); } },
      stderr: { write: (chunk) => { stderr += String(chunk); } },
    },
    get stdout() { return stdout; },
    get stderr() { return stderr; },
  };
}

function tempStateDir(name: string) {
  return fs.mkdtempSync(path.join(os.tmpdir(), `relaymux-${name}-`));
}

test("notify --suicide kills the current tmux window after notify succeeds", async () => {
  const harness = makeIo();
  const stateDir = tempStateDir("notify-suicide-success");
  const killedTargets: string[] = [];

  await handleNotify({
    flags: { runId: "run-1", message: "done", suicide: true },
    positionals: [],
    config: {},
    stateDir,
    io: harness.io,
    tmux: {
      killCurrentWindow: () => {
        killedTargets.push("agents:3");
        return { killed: true, target: "agents:3" };
      },
    },
  });

  assert.deepEqual(killedTargets, ["agents:3"]);
  assert.match(harness.stdout, /"runId":"run-1"/);
  assert.equal(harness.stderr, "");
  assert.match(fs.readFileSync(path.join(stateDir, "events.jsonl"), "utf8"), /"message":"done"/);
});

test("notify failure does not kill the current tmux window", async () => {
  const harness = makeIo();
  let killCalls = 0;

  await assert.rejects(
    () => handleNotify({
      flags: { replyMode: "bogus", message: "done", suicide: true },
      positionals: [],
      config: {},
      stateDir: tempStateDir("notify-suicide-failure"),
      io: harness.io,
      tmux: {
        killCurrentWindow: () => {
          killCalls += 1;
          return { killed: true, target: "agents:3" };
        },
      },
    }),
    /--reply-mode/,
  );

  assert.equal(killCalls, 0);
});

test("notify without --suicide does not touch tmux", async () => {
  const harness = makeIo();

  await handleNotify({
    flags: { runId: "run-2", message: "done" },
    positionals: [],
    config: {},
    stateDir: tempStateDir("notify-no-suicide"),
    io: harness.io,
    tmux: {
      killCurrentWindow: () => {
        throw new Error("tmux should not be called");
      },
    },
  });

  assert.match(harness.stdout, /"runId":"run-2"/);
  assert.equal(harness.stderr, "");
});

test("notify --suicide warns when the current tmux window cannot be resolved", async () => {
  const harness = makeIo();

  await handleNotify({
    flags: { runId: "run-3", message: "done", suicide: true },
    positionals: [],
    config: {},
    stateDir: tempStateDir("notify-suicide-warning"),
    io: harness.io,
    tmux: {
      killCurrentWindow: () => ({ killed: false, target: "", error: "not inside tmux" }),
    },
  });

  assert.match(harness.stdout, /"runId":"run-3"/);
  assert.match(harness.stderr, /--suicide requested/);
  assert.match(harness.stderr, /not inside tmux/);
  assert.match(harness.stderr, /leaving window open/);
});
