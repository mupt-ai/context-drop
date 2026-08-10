import type { LaunchRequest, RunRecord, RuntimeConfig } from "./types.js";
import { LaunchOutcomeUnknownError, prepareLaunch, systemRunner, type CommandRunner } from "./launch.js";

export function closeTmuxWorker(config: RuntimeConfig, run: RunRecord, runner: CommandRunner = systemRunner): boolean {
  if (run.backend !== "tmux" || !run.tmuxSession || !run.tmuxWindow) return false;
  const target = `${run.tmuxSession}:${run.tmuxWindow}`;
  const exists = runner.run("tmux", ["list-windows", "-t", run.tmuxSession, "-F", "#{window_name}"]);
  if (exists.status !== 0) return false;
  if (!(exists.stdout ?? "").split("\n").includes(run.tmuxWindow)) return true;
  return runner.run("tmux", ["kill-window", "-t", target]).status === 0;
}

export function launchInTmux(config: RuntimeConfig, request: LaunchRequest, id: string, runner: CommandRunner = systemRunner): RunRecord {
  const { name: window, argv, environment } = prepareLaunch(config, request, id);
  const envArgs = Object.entries(environment).flatMap(([key, value]) => ["-e", `${key}=${value}`]);
  let result = runner.run("tmux", ["has-session", "-t", config.tmuxSession]);
  if (result.status !== 0) {
    result = runner.run("tmux", ["new-session", "-d", "-s", config.tmuxSession, "-n", window, "-c", request.repo, ...envArgs, "--", ...argv]);
  } else {
    result = runner.run("tmux", ["new-window", "-d", "-t", config.tmuxSession, "-n", window, "-c", request.repo, ...envArgs, "--", ...argv]);
  }
  if (result.status !== 0) throw new LaunchOutcomeUnknownError(`tmux launch outcome is unknown: ${result.stderr || "unknown error"}`);
  return {
    id,
    name: window,
    agent: request.agent,
    repo: request.repo,
    backend: "tmux",
    tmuxSession: config.tmuxSession,
    tmuxWindow: window,
    lane: request.lane,
    status: "running",
    createdAt: new Date().toISOString(),
  };
}
