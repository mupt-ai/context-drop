import { basename } from "node:path";
import type { CommandRunner } from "./launch.js";
import { systemRunner } from "./launch.js";
import type { RuntimeConfig, SessionBackend } from "./types.js";

export interface LiveTaskStatus {
  runId?: string;
  paneId: string;
  agent: string;
  name: string;
  status: string;
  selected: boolean;
  fullyManaged: boolean;
}

interface HerdrAgentList {
  result?: {
    agents?: Array<{
      agent?: string;
      agent_status?: string;
      pane_id?: string;
      cwd?: string;
      foreground_cwd?: string;
      focused?: boolean;
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
  if (agents.some(agent => !agent || typeof agent.agent !== "string" || !agent.agent || typeof agent.agent_status !== "string" || !agent.agent_status || typeof agent.pane_id !== "string" || !agent.pane_id || (agent.cwd !== undefined && typeof agent.cwd !== "string") || (agent.foreground_cwd !== undefined && typeof agent.foreground_cwd !== "string") || (agent.focused !== undefined && typeof agent.focused !== "boolean"))) {
    throw new Error("live Herdr status returned malformed agent data");
  }
  return agents.map(agent => ({
    paneId: agent.pane_id!,
    agent: agent.agent!,
    name: basename(agent.foreground_cwd || agent.cwd || "") || `${agent.agent} task`,
    status: agent.agent_status!,
    selected: agent.focused === true,
    fullyManaged: false,
  }));
}

function liveTmuxTasks(config: RuntimeConfig, runner: CommandRunner): LiveTaskStatus[] {
  const separator = "\u001f";
  const format = ["#{pane_id}", "#{pane_current_command}", "#{pane_current_path}", "#{pane_dead}", "#{pane_active}"].join(separator);
  const listed = runner.run("tmux", ["list-panes", "-t", config.tmuxSession, "-F", format]);
  if (listed.status !== 0) throw new Error(`live tmux status is unavailable: ${listed.stderr || "list-panes failed"}`);
  return (listed.stdout ?? "").split("\n").filter(Boolean).map(line => {
    const [paneId, command, cwd, dead, active] = line.split(separator);
    if (!paneId?.startsWith("%") || command === undefined || cwd === undefined || (dead !== "0" && dead !== "1") || (active !== "0" && active !== "1")) throw new Error("live tmux status returned malformed pane data");
    const agent = command || "unknown";
    return { paneId, name: basename(cwd) || `${agent} task`, agent, status: dead === "1" ? "exited" : "running", selected: active === "1", fullyManaged: false };
  });
}

export function liveTaskStatus(config: RuntimeConfig, runner: CommandRunner = systemRunner, requestedBackend?: SessionBackend): { backend: SessionBackend; tasks: LiveTaskStatus[] } {
  const backend = requestedBackend ?? config.defaultBackend ?? "tmux";
  return { backend, tasks: backend === "herdr" ? liveHerdrTasks(config, runner) : liveTmuxTasks(config, runner) };
}
