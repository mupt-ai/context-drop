import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { quoteArgv } from "../src/command.js";
import { defaultConfig } from "../src/config.js";
import { resolveAgentConfig } from "../src/launch/agents.js";
import { launchTmuxAgent } from "../src/launch/tmux.js";

test("agent names resolve exactly from config", () => {
  const config = defaultConfig();

  const resolved = resolveAgentConfig(config, "claude");

  assert.equal(resolved.agentName, "claude");
  assert.deepEqual(resolved.agentConfig.command, config.agents.claude.command);
  assert.throws(() => resolveAgentConfig(config, "cc"), /Unknown agent "cc"/);
});

test("tmux launches the wrapper directly instead of typing into an interactive shell", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-launch-"));
  const binDir = path.join(root, "bin");
  const stateDir = path.join(root, "state");
  const logFile = path.join(root, "tmux.log");
  fs.mkdirSync(binDir);
  fs.writeFileSync(path.join(binDir, "tmux"), `#!/bin/sh
{
  printf 'CALL\\n'
  for arg in "$@"; do printf 'ARG:%s\\n' "$arg"; done
  printf 'END\\n'
} >> ${JSON.stringify(logFile)}
if [ "$1" = "new-window" ]; then printf 'agents:2.0\\n'; fi
`, { mode: 0o755 });

  let stdout = "";
  const originalPath = process.env.PATH;
  process.env.PATH = `${binDir}${path.delimiter}${originalPath || ""}`;
  try {
    const result = launchTmuxAgent({
      agentConfig: { command: ["/usr/bin/true"], promptMode: "none" },
      agentName: "custom",
      attach: false,
      cliPath: "/usr/local/bin/relaymux.js",
      configPath: path.join(root, "config.json"),
      dryRun: false,
      holdOnExit: false,
      io: { stdout: { write: (chunk) => { stdout += String(chunk); } } },
      launchNotification: undefined,
      name: "direct-launch",
      printCommand: false,
      prompt: "noop",
      quoteArgv,
      repo: root,
      runId: "run-direct-launch",
      session: "agents",
      sessionInfo: { session: "agents", mode: "shared", source: "config.session" },
      stateDir,
      workdir: root,
      worktreeAddArgs: undefined,
    });

    const calls = fs.readFileSync(logFile, "utf8");
    assert.ok(calls.includes(`ARG:/bin/sh ${result.scriptFile}\n`));
    assert.doesNotMatch(calls, /ARG:send-keys/);
    assert.match(stdout, /Started direct-launch/);
  } finally {
    process.env.PATH = originalPath;
  }
});
