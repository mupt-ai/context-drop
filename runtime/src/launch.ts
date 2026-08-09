import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { AgentConfig, LaunchRequest, RuntimeConfig } from "./types.js";

export interface CommandResult { status: number | null; stdout?: string; stderr?: string }
export class LaunchOutcomeUnknownError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "LaunchOutcomeUnknownError";
  }
}
export interface CommandRunner { run(command: string, args: string[]): CommandResult }
export const systemRunner: CommandRunner = {
  run(command, args) {
    const result = spawnSync(command, args, { encoding: "utf8" });
    return { status: result.status, stdout: result.stdout, stderr: result.stderr || result.error?.message };
  },
};

export function validateName(value: string): string {
  const cleaned = value.trim().replace(/[^A-Za-z0-9_.-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 60);
  if (!cleaned) throw new Error("invalid run name");
  return cleaned;
}

export function buildAgentArgv(agent: AgentConfig, promptFile: string): string[] {
  if (!Array.isArray(agent.command) || agent.command.length === 0 || agent.command.some(value => typeof value !== "string" || value.length === 0)) {
    throw new Error("agent command must be a non-empty argv array");
  }
  const promptMode = agent.promptMode ?? "arg";
  if (promptMode !== "arg") throw new Error("only arg prompt mode is supported in the MVP");
  let replaced = false;
  const argv = agent.command.map(value => value.replaceAll("{prompt_file}", () => {
    replaced = true;
    return promptFile;
  }));
  if (!replaced) argv.push(promptFile);
  return argv;
}

function validateEnvironment(environment: Record<string, string> | undefined): Record<string, string> {
  const result: Record<string, string> = {};
  for (const [key, value] of Object.entries(environment ?? {})) {
    if (!/^[A-Z_][A-Z0-9_]*$/.test(key) || typeof value !== "string" || value.includes("\0")) throw new Error("invalid launch environment");
    result[key] = value;
  }
  return result;
}

export function prepareLaunch(config: RuntimeConfig, request: LaunchRequest, id: string): { name: string; runDir: string; argv: string[]; environment: Record<string, string> } {
  const agent = config.agents[request.agent];
  if (!agent) throw new Error(`unknown agent: ${request.agent}`);
  if (!request.repo || !request.prompt) throw new Error("repo and prompt are required");
  const name = validateName(request.name || `${request.agent}-${id.slice(-8)}`);
  const runDir = join(config.stateDir, "runs", id);
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const promptFile = join(runDir, "prompt.txt");
  writeFileSync(promptFile, request.prompt, { mode: 0o600 });
  chmodSync(promptFile, 0o600);
  const argv = buildAgentArgv(agent, promptFile);
  if (request.extension) argv.push("--extension", request.extension);
  return { name, runDir, argv, environment: validateEnvironment(request.environment) };
}
