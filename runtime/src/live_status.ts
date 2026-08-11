import type { CommandRunner } from "./launch.js";
import { systemRunner } from "./launch.js";
import type { RuntimeConfig, SessionBackend } from "./types.js";
import { basename } from "node:path";

export interface LiveTaskStatus {
  label: string;
  agent?: string;
  status: string;
  cwd?: string;
  focused?: boolean;
}

interface HerdrAgentList {
  result?: {
    agents?: Array<{
      agent?: string;
      agent_status?: string;
      cwd?: string;
      foreground_cwd?: string;
      focused?: boolean;
      terminal_title_stripped?: string;
    }>;
  };
}

function liveHerdrTasks(config: RuntimeConfig, runner: CommandRunner): LiveTaskStatus[] {
  const herdr = config.herdrPath || "herdr";
  const session = config.herdrSession || "default";
  const listed = runner.run(herdr, ["--session", session, "agent", "list"]);
  if (listed.status !== 0) throw new Error(`live Herdr status is unavailable: ${listed.stderr || "agent list failed"}`);
  let response: HerdrAgentList;
  try {
    response = JSON.parse(listed.stdout ?? "") as HerdrAgentList;
  } catch {
    throw new Error("live Herdr status returned invalid JSON");
  }
  const agents = response.result?.agents;
  if (!Array.isArray(agents)) throw new Error("live Herdr status omitted the agent list");
  if (agents.some(agent => !agent || typeof agent.agent !== "string" || typeof agent.agent_status !== "string" || (agent.terminal_title_stripped !== undefined && typeof agent.terminal_title_stripped !== "string") || (agent.cwd !== undefined && typeof agent.cwd !== "string") || (agent.foreground_cwd !== undefined && typeof agent.foreground_cwd !== "string") || (agent.focused !== undefined && typeof agent.focused !== "boolean"))) {
    throw new Error("live Herdr status returned malformed agent data");
  }
  return agents.map(agent => ({
    label: basename(agent.foreground_cwd || agent.cwd || "unknown-project") || "unknown-project",
    agent: agent.agent,
    status: agent.agent_status!,
    focused: agent.focused === true,
  }));
}

function liveTmuxTasks(config: RuntimeConfig, runner: CommandRunner): LiveTaskStatus[] {
  const separator = "\u001f";
  const format = ["#{window_name}", "#{pane_current_command}", "#{pane_current_path}", "#{pane_dead}"].join(separator);
  const listed = runner.run("tmux", ["list-panes", "-t", config.tmuxSession, "-F", format]);
  if (listed.status !== 0) throw new Error(`live tmux status is unavailable: ${listed.stderr || "list-panes failed"}`);
  return (listed.stdout ?? "").split("\n").filter(Boolean).map(line => {
    const [window, command, cwd, dead] = line.split(separator);
    if (!window || command === undefined || cwd === undefined || (dead !== "0" && dead !== "1")) throw new Error("live tmux status returned malformed pane data");
    return { label: basename(cwd) || window, agent: command || undefined, status: dead === "1" ? "exited" : "running" };
  });
}

export function liveTaskStatus(config: RuntimeConfig, runner: CommandRunner = systemRunner): { backend: SessionBackend; tasks: LiveTaskStatus[] } {
  const backend = config.defaultBackend ?? "tmux";
  return { backend, tasks: backend === "herdr" ? liveHerdrTasks(config, runner) : liveTmuxTasks(config, runner) };
}
