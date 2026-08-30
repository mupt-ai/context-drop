import type { LaunchRequest, RunRecord, RuntimeConfig } from "./types.js";
import { LaunchOutcomeUnknownError, prepareLaunch, systemRunner, type CommandRunner } from "./launch.js";

export function tmuxWorkerAlive(config: RuntimeConfig, run: RunRecord, runner: CommandRunner = systemRunner): boolean | undefined {
  if (run.backend !== "tmux" || !run.tmuxSession || !run.tmuxWindow) return false;
  const listed = runner.run("tmux", ["list-windows", "-t", run.tmuxSession, "-F", "#{window_name}"]);
  if (listed.status !== 0) return undefined;
  return (listed.stdout ?? "").split("\n").includes(run.tmuxWindow);
}

export function continueLiveTmux(config: RuntimeConfig, paneId: string, followUp: string, runner: CommandRunner = systemRunner): void {
  if (!/^%[0-9]+$/.test(paneId)) throw new Error("invalid tmux pane ID");
  const listed = runner.run("tmux", ["list-panes", "-a", "-F", "#{pane_id}"]); if (listed.status !== 0) throw new Error(`live tmux status is unavailable: ${listed.stderr || "list-panes failed"}`);
  if (!(listed.stdout ?? "").split("\n").includes(paneId)) throw new Error("tmux pane is no longer available");
  const sent = runner.run("tmux", ["send-keys", "-t", paneId, "-l", followUp]); if (sent.status !== 0) throw new Error(`delegated task follow-up was not sent: ${sent.stderr || "send-keys failed"}`);
  const entered = runner.run("tmux", ["send-keys", "-t", paneId, "Enter"]); if (entered.status !== 0) throw new LaunchOutcomeUnknownError(`delegated task follow-up outcome is unknown: ${entered.stderr || "Enter failed"}`);
}

export function continueInTmux(config: RuntimeConfig, run: RunRecord, message: string, runner: CommandRunner = systemRunner): void {
  const alive = tmuxWorkerAlive(config, run, runner);
  if (alive === false) throw new Error("delegated task window is no longer available");
  if (alive !== true || !run.tmuxSession || !run.tmuxWindow) throw new Error("delegated task tmux state could not be confirmed");
  const target = `${run.tmuxSession}:${run.tmuxWindow}`;
  const sent = runner.run("tmux", ["send-keys", "-t", target, "-l", message]);
  if (sent.status !== 0) throw new Error(`delegated task follow-up was not sent: ${sent.stderr || "send-keys failed"}`);
  const entered = runner.run("tmux", ["send-keys", "-t", target, "Enter"]);
  if (entered.status !== 0) throw new LaunchOutcomeUnknownError(`delegated task follow-up outcome is unknown: ${entered.stderr || "Enter failed"}`);
}

export function readTmuxWorker(run: RunRecord, runner: CommandRunner = systemRunner): string {
  const target = run.tmuxPane || (run.tmuxSession && run.tmuxWindow ? `${run.tmuxSession}:${run.tmuxWindow}` : "");
  if (!target) throw new Error("task has no persisted tmux pane");
  const captured = runner.run("tmux", ["capture-pane", "-p", "-J", "-S", "-500", "-t", target]);
  if (captured.status !== 0) throw new Error(`tmux pane capture failed: ${captured.stderr || "unknown error"}`);
  return captured.stdout ?? "";
}

export function closeTmuxWorker(config: RuntimeConfig, run: RunRecord, runner: CommandRunner = systemRunner): boolean {
  if (run.backend !== "tmux" || !run.tmuxSession || !run.tmuxWindow) return false;
  const target = `${run.tmuxSession}:${run.tmuxWindow}`;
  const alive = tmuxWorkerAlive(config, run, runner);
  if (alive === false) return true;
  if (alive !== true) return false;
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
  const retained = runner.run("tmux", ["set-option", "-t", `${config.tmuxSession}:${window}`, "remain-on-exit", "on"]);
  if (retained.status !== 0) throw new LaunchOutcomeUnknownError(`tmux output retention outcome is unknown: ${retained.stderr || "set-option failed"}`);
  const pane = runner.run("tmux", ["list-panes", "-t", `${config.tmuxSession}:${window}`, "-F", "#{pane_id}"]);
  const tmuxPane = pane.status === 0 ? (pane.stdout ?? "").trim().split("\n")[0] : undefined;
  if (!tmuxPane?.startsWith("%")) throw new LaunchOutcomeUnknownError(`tmux launch pane outcome is unknown: ${pane.stderr || "pane ID unavailable"}`);
  return {
    id,
    name: window,
    agent: request.agent,
    repo: request.repo,
    backend: "tmux",
    tmuxSession: config.tmuxSession,
    tmuxWindow: window,
    tmuxPane,
    lane: request.lane,
    finalMarker: request.finalMarker,
    finalOutputPath: request.finalOutputPath,
    status: "running",
    createdAt: new Date().toISOString(),
  };
}
