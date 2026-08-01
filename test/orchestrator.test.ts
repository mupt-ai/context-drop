import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { defaultConfig } from "../src/config.js";
import { buildIncomingOrchestratorPrompt, buildTerminalOrchestratorParts, buildTerminalOrchestratorPrompt, joinPromptParts, runOrchestrator } from "../src/orchestrator.js";

function terminalJob(overrides: Record<string, any> = {}) {
  return {
    source: "cli",
    requestId: "req-1",
    receivedAt: "2026-06-16T00:00:00.000Z",
    replyMode: "none",
    metadata: {},
    text: "inspect the failing tests",
    ...overrides,
  };
}

test("orchestrator requests include repo-managed best practices by default", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-default-"));
  const config = defaultConfig({ RELAYMUX_HOME: path.join(dir, "home") });
  const prompt = buildTerminalOrchestratorPrompt({
    config,
    configPath: path.join(dir, "config.json"),
    job: terminalJob(),
  });

  assert.match(prompt, /You are a local relaymux orchestrator/);
  assert.match(prompt, /Delegate by default when the work may take more than about 10 seconds/);
  assert.match(prompt, /repo code changes, PR fixes, deploy\/debugging work, deep research, CI loops, docs rewrites, long validation, and multi-file edits/);
  assert.match(prompt, /Do truly tiny replies and lightweight read-only inspection inline/);
  assert.match(prompt, /durable relaymux records in the relaymux SQLite DB/);
  assert.match(prompt, /file artifacts and implementation notes belong under state\//);
  assert.match(prompt, /Do not add --session or --session-mode/);
  assert.match(prompt, /inspect relaymux status and the tmux window\/pane output before claiming that it started/);
  assert.match(prompt, /belongs to an existing active subagent\/tab, send the instruction to that tab instead of launching a duplicate run/);
  assert.match(prompt, /Do not use one-shot model print-mode or non-interactive shortcuts as a substitute/);
  assert.match(prompt, /Include an idempotency key/);
  assert.match(prompt, /prefer an isolated branch or worktree/);
  assert.match(prompt, /# Terminal request/);
});

test("orchestrator requests include relaymux home AGENTS.md when present", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-agents-"));
  const home = path.join(dir, "home");
  fs.mkdirSync(home, { recursive: true });
  fs.writeFileSync(path.join(home, "AGENTS.md"), "Local relaymux AGENTS instructions.");
  const config = defaultConfig({ RELAYMUX_HOME: home });

  const prompt = buildTerminalOrchestratorPrompt({
    config,
    configPath: path.join(home, "config.json"),
    job: terminalJob(),
  });

  assert.match(prompt, /You are a local relaymux orchestrator/);
  assert.match(prompt, /Local relaymux AGENTS instructions/);
  assert.ok(prompt.indexOf("You are a local relaymux orchestrator") < prompt.indexOf("Local relaymux AGENTS instructions"));
});

test("orchestrator prompt file and extra prompt remain additive", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-custom-"));
  const home = path.join(dir, "home");
  const customPromptFile = path.join(dir, "custom-system-prompt.md");
  fs.mkdirSync(home, { recursive: true });
  fs.writeFileSync(path.join(home, "AGENTS.md"), "Home AGENTS prompt should not appear.");
  fs.writeFileSync(customPromptFile, "Local custom system prompt.");
  const config = defaultConfig({ RELAYMUX_HOME: home });
  config.orchestrator.systemPromptFile = customPromptFile;
  config.orchestrator.extraSystemPrompt = "Extra local prompt.";

  const prompt = buildIncomingOrchestratorPrompt({
    config,
    configPath: path.join(dir, "config.json"),
    incomingText: "hello",
  });

  assert.match(prompt, /You are a local relaymux orchestrator/);
  assert.match(prompt, /Local custom system prompt/);
  assert.match(prompt, /Extra local prompt/);
  assert.doesNotMatch(prompt, /Home AGENTS prompt should not appear/);
  assert.ok(prompt.indexOf("You are a local relaymux orchestrator") < prompt.indexOf("Local custom system prompt"));
  assert.ok(prompt.indexOf("Local custom system prompt") < prompt.indexOf("Extra local prompt"));
});

test("orchestrator includes SOUL.md only when present or explicitly configured", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-soul-"));
  const home = path.join(dir, "home");
  const config = defaultConfig({ RELAYMUX_HOME: home });

  let prompt = buildTerminalOrchestratorPrompt({
    config,
    configPath: path.join(home, "config.json"),
    job: terminalJob(),
  });
  assert.doesNotMatch(prompt, /Local personality prompt/);

  fs.mkdirSync(home, { recursive: true });
  fs.writeFileSync(path.join(home, "SOUL.md"), "Local personality prompt.");
  prompt = buildTerminalOrchestratorPrompt({
    config,
    configPath: path.join(home, "config.json"),
    job: terminalJob(),
  });
  assert.match(prompt, /Local personality prompt/);

  const configuredSoul = path.join(dir, "configured-personality.md");
  fs.writeFileSync(configuredSoul, "Configured personality prompt.");
  config.orchestrator.personalityPromptFile = configuredSoul;
  prompt = buildTerminalOrchestratorPrompt({
    config,
    configPath: path.join(home, "config.json"),
    job: terminalJob(),
  });
  assert.match(prompt, /Configured personality prompt/);
  assert.doesNotMatch(prompt, /Local personality prompt/);
});

test("orchestrator does not read global Pi AGENTS.md", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-no-pi-agents-"));
  const home = path.join(dir, "home");
  const userHome = path.join(dir, "user-home");
  const piAgentDir = path.join(userHome, ".pi", "agent");
  fs.mkdirSync(piAgentDir, { recursive: true });
  fs.writeFileSync(path.join(piAgentDir, "AGENTS.md"), "Global Pi AGENTS marker.");
  const config = defaultConfig({ RELAYMUX_HOME: home });
  const oldHome = process.env.HOME;

  try {
    process.env.HOME = userHome;
    const prompt = buildTerminalOrchestratorPrompt({
      config,
      configPath: path.join(home, "config.json"),
      job: terminalJob(),
    });

    assert.doesNotMatch(prompt, /Global Pi AGENTS marker/);
  } finally {
    if (oldHome === undefined) {
      delete process.env.HOME;
    } else {
      process.env.HOME = oldHome;
    }
  }
});

test("orchestrator default system prompt can be disabled", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-disable-"));
  const config = defaultConfig({ RELAYMUX_HOME: path.join(dir, "home") });
  config.orchestrator.defaultSystemPrompt = false;
  config.orchestrator.extraSystemPrompt = "Only this local prompt.";

  const prompt = buildTerminalOrchestratorPrompt({
    config,
    configPath: path.join(dir, "config.json"),
    job: terminalJob(),
  });

  assert.doesNotMatch(prompt, /You are a local relaymux orchestrator/);
  assert.match(prompt, /Only this local prompt/);
  assert.match(prompt, /Runtime context:/);
  assert.match(prompt, /# Terminal request/);
});

test("buildPromptParts splits system prompt from user turn", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-parts-"));
  const config = defaultConfig({ RELAYMUX_HOME: path.join(dir, "home") });
  const parts = buildTerminalOrchestratorParts({
    config,
    configPath: path.join(dir, "config.json"),
    job: terminalJob(),
  });

  assert.match(parts.systemPrompt, /You are a local relaymux orchestrator/);
  assert.match(parts.systemPrompt, /Runtime context:/);
  assert.doesNotMatch(parts.systemPrompt, /# Terminal request/);
  assert.match(parts.userPrompt, /# Terminal request/);
  assert.doesNotMatch(parts.userPrompt, /You are a local relaymux orchestrator/);
  assert.equal(
    joinPromptParts(parts),
    buildTerminalOrchestratorPrompt({ config, configPath: path.join(dir, "config.json"), job: terminalJob() }),
  );
});

test("runOrchestrator writes a separate system prompt file when command uses {systemPromptFile}", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-run-"));
  const stateDir = path.join(dir, "state");
  fs.mkdirSync(stateDir, { recursive: true });
  const config = defaultConfig({ RELAYMUX_HOME: path.join(dir, "home") });
  config.orchestrator = {
    ...config.orchestrator,
    cwd: dir,
    command: ["/bin/echo", "--system-prompt", "{systemPromptFile}", "{prompt}"],
    promptMode: "arg",
  };
  const parts = buildTerminalOrchestratorParts({
    config,
    configPath: path.join(dir, "config.json"),
    job: terminalJob(),
  });

  await runOrchestrator(config, {
    prompt: joinPromptParts(parts),
    promptParts: parts,
    stateDir,
    configPath: path.join(dir, "config.json"),
    requestId: "req-split",
  });

  const promptFiles = fs.readdirSync(path.join(stateDir, "prompts"));
  assert.ok(promptFiles.includes("req-split.orchestrator.txt"), "user prompt file written");
  assert.ok(promptFiles.includes("req-split.orchestrator.system.txt"), "system prompt file written");
  const systemFile = fs.readFileSync(path.join(stateDir, "prompts", "req-split.orchestrator.system.txt"), "utf8");
  const userFile = fs.readFileSync(path.join(stateDir, "prompts", "req-split.orchestrator.txt"), "utf8");
  assert.match(systemFile, /You are a local relaymux orchestrator/);
  assert.match(userFile, /# Terminal request/);
  assert.doesNotMatch(userFile, /You are a local relaymux orchestrator/);
});

test("runOrchestrator automatically injects system prompt file into any command with {prompt}", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-orchestrator-auto-"));
  const stateDir = path.join(dir, "state");
  fs.mkdirSync(stateDir, { recursive: true });
  const config = defaultConfig({ RELAYMUX_HOME: path.join(dir, "home") });
  config.orchestrator = {
    ...config.orchestrator,
    cwd: dir,
    command: ["/bin/echo", "{prompt}"],
    promptMode: "arg",
  };
  const parts = buildTerminalOrchestratorParts({
    config,
    configPath: path.join(dir, "config.json"),
    job: terminalJob(),
  });

  await runOrchestrator(config, {
    prompt: joinPromptParts(parts),
    promptParts: parts,
    stateDir,
    configPath: path.join(dir, "config.json"),
    requestId: "req-auto",
  });

  const promptFiles = fs.readdirSync(path.join(stateDir, "prompts"));
  assert.ok(promptFiles.some((f) => f.includes(".system")), "system prompt file written automatically");
  const userFile = fs.readFileSync(path.join(stateDir, "prompts", "req-auto.orchestrator.txt"), "utf8");
  assert.doesNotMatch(userFile, /You are a local relaymux orchestrator/);
  assert.match(userFile, /# Terminal request/);
});
