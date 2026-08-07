import type { LaunchRequest, RunRecord, RuntimeConfig } from "./types.js";
import { prepareLaunch, systemRunner, type CommandRunner } from "./launch.js";

export function launchInTmux(config: RuntimeConfig, request: LaunchRequest, id: string, runner: CommandRunner = systemRunner): RunRecord {
  const { name: window, argv } = prepareLaunch(config, request, id);
  let result = runner.run("tmux", ["has-session", "-t", config.tmuxSession]);
  if (result.status !== 0) {
    result = runner.run("tmux", ["new-session", "-d", "-s", config.tmuxSession, "-n", window, "-c", request.repo, "--", ...argv]);
  } else {
    result = runner.run("tmux", ["new-window", "-d", "-t", config.tmuxSession, "-n", window, "-c", request.repo, "--", ...argv]);
  }
  if (result.status !== 0) throw new Error(`tmux launch failed: ${result.stderr || "unknown error"}`);
  return {
    id,
    name: window,
    agent: request.agent,
    repo: request.repo,
    backend: "tmux",
    tmuxSession: config.tmuxSession,
    tmuxWindow: window,
    status: "running",
    createdAt: new Date().toISOString(),
  };
}
