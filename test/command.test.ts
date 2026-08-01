import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import {
  buildAgentInvocation,
  buildTmuxShellScript,
  quoteArgv,
  renderTemplate,
  shellExportBlock,
  shellQuote,
} from "../src/command.js";

test("shellQuote keeps safe tokens and quotes spaces", () => {
  assert.equal(shellQuote("abc-123_/x"), "abc-123_/x");
  assert.equal(shellQuote("hello world"), "'hello world'");
  assert.equal(shellQuote("can't"), "'can'\\''t'");
});

test("quoteArgv quotes each token independently", () => {
  assert.equal(quoteArgv(["codex", "--prompt", "review api"]), "codex --prompt 'review api'");
});

test("renderTemplate replaces known placeholders only", () => {
  assert.equal(renderTemplate("{agent}:{repo}:{missing}", { agent: "codex", repo: "/r" }), "codex:/r:{missing}");
});

test("buildAgentInvocation renders command templates without duplicate prompt", () => {
  const invocation = buildAgentInvocation("codex", {
    command: ["codex", "{prompt}", "--repo", "{repo}"],
    promptMode: "arg",
  }, {
    prompt: "do work",
    promptFile: "/tmp/prompt",
    repo: "/repo",
  });

  assert.deepEqual(invocation.argv, ["codex", "do work", "--repo", "/repo"]);
  assert.equal(invocation.stdinFile, null);
});

test("buildAgentInvocation supports stdin prompt mode", () => {
  const invocation = buildAgentInvocation("agent", {
    command: ["agent"],
    promptMode: "stdin",
  }, {
    prompt: "do work",
    promptFile: "/tmp/prompt",
  });

  assert.deepEqual(invocation.argv, ["agent"]);
  assert.equal(invocation.stdinFile, "/tmp/prompt");
});

test("buildAgentInvocation renders systemPrompt/systemPromptFile placeholders", () => {
  const invocation = buildAgentInvocation("orchestrator", {
    command: ["pi", "--print", "--system-prompt", "{systemPromptFile}", "{prompt}"],
    promptMode: "arg",
  }, {
    prompt: "user turn body",
    promptFile: "/tmp/prompt",
    systemPrompt: "system text",
    systemPromptFile: "/tmp/system",
  });

  assert.deepEqual(invocation.argv, ["pi", "--print", "--system-prompt", "/tmp/system", "user turn body"]);
  assert.equal(invocation.stdinFile, null);
});

test("shellExportBlock rejects invalid env keys", () => {
  assert.throws(() => shellExportBlock({ "BAD-KEY": "value" }), /Invalid environment/);
});

test("tmux shell script can auto notify on nonzero exit", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-script-"));
  const notifyLog = path.join(dir, "notify.log");
  const fakeCli = path.join(dir, "relaymux-fake.js");
  const scriptFile = path.join(dir, "run.sh");
  fs.writeFileSync(fakeCli, `import fs from "node:fs";\nfs.appendFileSync(${JSON.stringify(notifyLog)}, JSON.stringify(process.argv.slice(2)) + "\\n");\n`);

  const script = buildTmuxShellScript({
    argv: ["sh", "-c", "printf 'bad flag\\n' >&2; exit 7"],
    env: {},
    stdinFile: null,
  }, {
    agent: "codex",
    cliPath: fakeCli,
    configPath: path.join(dir, "config.json"),
    holdOnExit: false,
    launchNotification: { onExit: "failure", replyMode: "imessage", tailLines: 20, tailBytes: 1000 },
    name: "bad-codex",
    promptFile: path.join(dir, "prompt.txt"),
    repo: dir,
    runId: "run-test",
    session: "agents",
    workdir: dir,
  });
  fs.writeFileSync(scriptFile, script, { mode: 0o700 });

  const result = spawnSync("/bin/sh", [scriptFile], { encoding: "utf8", env: { ...process.env, TMUX_PANE: "" } });
  assert.equal(result.status, 7);
  assert.match(result.stderr, /bad flag/);

  const calls = fs.readFileSync(notifyLog, "utf8").trim().split("\n").map((line) => JSON.parse(line));
  assert.equal(calls.length, 3);
  assert.deepEqual(calls[0].slice(2, 5), ["notify", "--run-id", "run-test"]);
  assert.ok(calls[1].includes("--message"));
  assert.ok(calls[2].includes("--reply-mode"));
  assert.ok(calls[2].includes("imessage"));
  assert.ok(calls[2].includes("--idempotency-key"));
  assert.ok(calls[2].includes("run-test-exit-7"));
  assert.ok(calls[2].includes("relaymux run bad-codex (run-test) failed with exit 7"));
});
