import { runCommand } from "./process.js";

const WINDOW_FIELD_SEPARATOR = "<|relaymux|>";
const WINDOW_FORMAT = [
  "#{session_name}",
  "#{window_index}",
  "#{window_name}",
  "#{window_active}",
  "#{window_panes}",
  "#{pane_current_path}",
  "#{@relaymux}",
  "#{@relaymux_run_id}",
  "#{@relaymux_agent}",
  "#{@relaymux_repo}",
  "#{@relaymux_name}",
  "#{@relaymux_started}",
].join(WINDOW_FIELD_SEPARATOR);

export function validateSessionName(session) {
  if (!/^[A-Za-z0-9_.-]+$/.test(session)) {
    throw new Error(`Invalid tmux session name "${session}". Use letters, numbers, dot, dash, or underscore.`);
  }
}

export function hasSession(session) {
  const result = runCommand("tmux", ["has-session", "-t", session], { allowFailure: true });
  return result.status === 0;
}

export function createAgentWindow({ session, name, cwd }) {
  validateSessionName(session);

  return createWindow({ session, name, cwd });
}

export function createCommandWindow({ session, name, cwd, shellCommand }) {
  validateSessionName(session);

  return createWindow({ session, name, cwd, shellCommand });
}

export function createCommandPane({ windowTarget, cwd, shellCommand, split = "horizontal" }) {
  const args = ["split-window", "-d", "-P", "-F", "#{session_name}:#{window_index}.#{pane_index}"];
  if (split === "vertical") {
    args.push("-v");
  } else {
    args.push("-h");
  }
  args.push("-t", windowTarget, "-c", cwd, shellCommand);

  const result = runCommand("tmux", args);
  const target = result.stdout.trim();
  return {
    target,
    windowTarget: target.replace(/\.\d+$/, ""),
  };
}

function createWindow({ session, name, cwd, shellCommand = undefined }) {
  const args = hasSession(session)
    ? ["new-window", "-d", "-P", "-F", "#{session_name}:#{window_index}.#{pane_index}", "-t", `${session}:`, "-n", name, "-c", cwd]
    : ["new-session", "-d", "-P", "-F", "#{session_name}:#{window_index}.#{pane_index}", "-s", session, "-n", name, "-c", cwd];

  if (shellCommand !== undefined) {
    args.push(shellCommand);
  }

  const result = runCommand("tmux", args);
  const target = result.stdout.trim();
  return {
    target,
    windowTarget: target.replace(/\.\d+$/, ""),
  };
}

export function killWindowByName({ session, name }) {
  validateSessionName(session);
  const result = runCommand("tmux", ["kill-window", "-t", `${session}:${name}`], { allowFailure: true });
  return result.status === 0;
}

export function resolveCurrentWindowTarget({ env = process.env }: any = {}) {
  if (!env?.TMUX || !env?.TMUX_PANE) {
    return {
      ok: false,
      error: "not inside tmux (TMUX/TMUX_PANE is not set)",
    };
  }

  const result = runCommand("tmux", ["display-message", "-p", "-t", String(env.TMUX_PANE), "#S:#I"], { allowFailure: true, env });
  if (result.status !== 0) {
    return {
      ok: false,
      error: tmuxFailureMessage(result, "tmux display-message could not resolve the current window target"),
    };
  }

  const target = result.stdout.trim().split("\n")[0]?.trim();
  if (!target) {
    return {
      ok: false,
      error: "tmux display-message did not return a current window target",
    };
  }

  return { ok: true, target };
}

export function killCurrentWindow({ env = process.env }: any = {}) {
  const resolved = resolveCurrentWindowTarget({ env });
  if (!resolved.ok) {
    return {
      killed: false,
      target: "",
      error: resolved.error,
    };
  }

  const result = runCommand("tmux", ["kill-window", "-t", resolved.target], { allowFailure: true, env });
  if (result.status !== 0) {
    return {
      killed: false,
      target: resolved.target,
      error: tmuxFailureMessage(result, `tmux kill-window failed for ${resolved.target}`),
    };
  }

  return { killed: true, target: resolved.target };
}

function tmuxFailureMessage(result, fallback) {
  if (result.error?.message) return result.error.message;
  const stderr = result.stderr?.trim();
  if (stderr) return stderr;
  return fallback;
}

export function selectLayout(target, layout = "tiled") {
  runCommand("tmux", ["select-layout", "-t", target, layout], { allowFailure: true });
}

export function sendShellCommand(target, shellCommand) {
  runCommand("tmux", ["send-keys", "-t", target, "-l", shellCommand]);
  runCommand("tmux", ["send-keys", "-t", target, "C-m"]);
}

export function setWindowMetadata(windowTarget, metadata) {
  for (const [key, value] of Object.entries(metadata)) {
    runCommand("tmux", ["set-option", "-w", "-t", windowTarget, `@${key}`, String(value)]);
  }
}

export function listAgentWindows({ session }: any = {}) {
  const args = session
    ? ["list-windows", "-t", session, "-F", WINDOW_FORMAT]
    : ["list-windows", "-a", "-F", WINDOW_FORMAT];
  const result = runCommand("tmux", args, { allowFailure: true });
  if (result.status !== 0) {
    return [];
  }

  return result.stdout
    .split("\n")
    .filter(Boolean)
    .map(parseWindowLine)
    .filter((window) => window.relaymux === "1");
}

function parseWindowLine(line) {
  const [
    session,
    windowIndex,
    windowName,
    active,
    panes,
    cwd,
    relaymux,
    runId,
    agent,
    repo,
    name,
    started,
  ] = line.split(WINDOW_FIELD_SEPARATOR);

  return {
    session,
    windowIndex,
    windowName,
    active: active === "1",
    panes: Number(panes),
    cwd,
    relaymux,
    runId,
    agent,
    repo,
    name,
    started,
    target: `${session}:${windowIndex}`,
  };
}
