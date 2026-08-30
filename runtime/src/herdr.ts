import { chmodSync, realpathSync, statSync, writeFileSync } from "node:fs";
import { isAbsolute, join } from "node:path";
import type { LaunchRequest, RunRecord, RuntimeConfig } from "./types.js";
import { LaunchOutcomeUnknownError, prepareLaunch, systemRunner, type CommandRunner } from "./launch.js";

interface HerdrWorkspaceCreated {
  result?: {
    workspace?: { workspace_id?: string };
    tab?: { tab_id?: string };
    root_pane?: { pane_id?: string };
  };
}

interface HerdrWorkspaceList {
  result?: {
    workspaces?: Array<{ workspace_id?: string; label?: string; focused?: boolean }>;
  };
}

interface WorkspaceLocation {
  workspace: string;
  tab: string;
  pane: string;
  createdFreshWorkspace: boolean;
}

export interface HerdrAgentInfo {
  paneId: string;
  workspaceId: string;
  tabId: string;
  agent: string;
  status: string;
  cwd?: string;
  foregroundCwd?: string;
  focused: boolean;
  title?: string;
}
export interface HerdrPaneInfo {
  paneId: string;
  workspaceId: string;
  tabId: string;
  agent?: string;
  status?: string;
  cwd?: string;
  foregroundCwd?: string;
  focused: boolean;
  title?: string;
}
export interface HerdrTopology {
  session: string;
  workspaces: Array<{ workspaceId: string; label: string; focused: boolean; status?: string }>;
  tabs: Array<{ tabId: string; workspaceId: string; label: string; focused: boolean; status?: string }>;
  panes: HerdrPaneInfo[];
  agents: HerdrAgentInfo[];
}

function herdrJSON<T>(config: RuntimeConfig, args: string[], description: string, runner: CommandRunner): T {
  const result = runner.run(config.herdrPath || "herdr", ["--session", config.herdrSession || "default", ...args]);
  if (result.status !== 0) throw new Error(`${description} failed: ${result.stderr || "unknown error"}`);
  try { return JSON.parse(result.stdout ?? "") as T; } catch { throw new Error(`${description} returned invalid JSON`); }
}

export function listHerdrAgents(config: RuntimeConfig, runner: CommandRunner = systemRunner): HerdrAgentInfo[] {
  const agentResult = herdrJSON<{ result?: { agents?: unknown } }>(config, ["agent", "list"], "herdr agent list", runner).result?.agents;
  if (!Array.isArray(agentResult)) throw new Error("herdr agent list omitted required list");
  return agentResult.map((value: any) => {
    if (!value || typeof value.pane_id !== "string" || typeof value.agent !== "string" || typeof value.agent_status !== "string" || typeof value.focused !== "boolean") throw new Error("herdr agent list returned malformed agent data");
    return { paneId: value.pane_id, workspaceId: typeof value.workspace_id === "string" ? value.workspace_id : value.pane_id.split(":")[0], tabId: typeof value.tab_id === "string" ? value.tab_id : "", agent: value.agent, status: value.agent_status, focused: value.focused, ...(typeof value.cwd === "string" ? { cwd: value.cwd } : {}), ...(typeof value.foreground_cwd === "string" ? { foregroundCwd: value.foreground_cwd } : {}), ...(typeof value.terminal_title_stripped === "string" ? { title: value.terminal_title_stripped } : {}) };
  });
}

export function herdrTopology(config: RuntimeConfig, runner: CommandRunner = systemRunner): HerdrTopology {
  type Raw = { result?: Record<string, unknown> };
  const workspaceResult = herdrJSON<Raw>(config, ["workspace", "list"], "herdr workspace list", runner).result?.workspaces;
  const tabResult = herdrJSON<Raw>(config, ["tab", "list"], "herdr tab list", runner).result?.tabs;
  const paneResult = herdrJSON<Raw>(config, ["pane", "list"], "herdr pane list", runner).result?.panes;
  if (!Array.isArray(workspaceResult) || !Array.isArray(tabResult) || !Array.isArray(paneResult)) throw new Error("herdr topology response omitted required lists");
  const workspaces = workspaceResult.map((value: any) => {
    if (!value || typeof value.workspace_id !== "string" || typeof value.label !== "string" || typeof value.focused !== "boolean") throw new Error("herdr topology returned malformed workspace data");
    return { workspaceId: value.workspace_id, label: value.label, focused: value.focused, ...(typeof value.agent_status === "string" ? { status: value.agent_status } : {}) };
  });
  const tabs = tabResult.map((value: any) => {
    if (!value || typeof value.tab_id !== "string" || typeof value.workspace_id !== "string" || typeof value.label !== "string" || typeof value.focused !== "boolean") throw new Error("herdr topology returned malformed tab data");
    return { tabId: value.tab_id, workspaceId: value.workspace_id, label: value.label, focused: value.focused, ...(typeof value.agent_status === "string" ? { status: value.agent_status } : {}) };
  });
  const panes = paneResult.map((value: any) => {
    if (!value || typeof value.pane_id !== "string" || typeof value.workspace_id !== "string" || typeof value.tab_id !== "string" || typeof value.focused !== "boolean") throw new Error("herdr topology returned malformed pane data");
    return { paneId: value.pane_id, workspaceId: value.workspace_id, tabId: value.tab_id, focused: value.focused, ...(typeof value.agent === "string" ? { agent: value.agent } : {}), ...(typeof value.agent_status === "string" ? { status: value.agent_status } : {}), ...(typeof value.cwd === "string" ? { cwd: value.cwd } : {}), ...(typeof value.foreground_cwd === "string" ? { foregroundCwd: value.foreground_cwd } : {}), ...(typeof value.terminal_title_stripped === "string" ? { title: value.terminal_title_stripped } : {}) };
  });
  return { session: config.herdrSession || "default", workspaces, tabs, panes, agents: listHerdrAgents(config, runner) };
}

function requireLiveAgent(config: RuntimeConfig, paneId: string, runner: CommandRunner): HerdrAgentInfo {
  if (!/^w[^:]+:p[^:]+$/.test(paneId)) throw new Error("invalid Herdr pane ID");
  const agent = listHerdrAgents(config, runner).find(item => item.paneId === paneId);
  if (!agent) throw new Error("Herdr agent is no longer available");
  return agent;
}

export function readHerdrAgent(config: RuntimeConfig, paneId: string, lines: number, runner: CommandRunner = systemRunner): string {
  requireLiveAgent(config, paneId, runner);
  if (!Number.isInteger(lines) || lines < 1 || lines > 500) throw new Error("lines must be between 1 and 500");
  const result = runner.run(config.herdrPath || "herdr", ["--session", config.herdrSession || "default", "agent", "read", paneId, "--source", "recent-unwrapped", "--lines", String(lines), "--format", "text"]);
  if (result.status !== 0) throw new Error(`herdr agent read failed: ${result.stderr || "unknown error"}`);
  return result.stdout ?? "";
}

export function readHerdrPane(config: RuntimeConfig, run: RunRecord, lines: number, runner: CommandRunner = systemRunner): string {
  if (!run.herdrSession || !run.herdrPane) throw new Error("task has no persisted Herdr pane");
  if (!Number.isInteger(lines) || lines < 1 || lines > 500) throw new Error("lines must be between 1 and 500");
  const result = runner.run(config.herdrPath || "herdr", ["--session", run.herdrSession, "pane", "read", run.herdrPane, "--source", "recent-unwrapped", "--lines", String(lines), "--format", "text"]);
  if (result.status !== 0) throw new Error(`herdr pane read failed: ${result.stderr || "unknown error"}`);
  return result.stdout ?? "";
}

export function herdrAgentStatus(config: RuntimeConfig, paneId: string, runner: CommandRunner = systemRunner): { paneId: string; status: string } {
  const agent = requireLiveAgent(config, paneId, runner);
  if (!new Set(["idle", "working", "blocked", "done", "unknown"]).has(agent.status)) throw new Error("Herdr returned an invalid lifecycle status");
  return { paneId: agent.paneId, status: agent.status };
}

function existingDirectory(path: string): string {
  if (!isAbsolute(path)) throw new Error("repository path must be absolute");
  let real: string;
  try { real = realpathSync(path); } catch { throw new Error("repository path does not exist"); }
  if (!statSync(real).isDirectory()) throw new Error("repository path is not a directory");
  return real;
}

export function resolveHerdrRepo(config: RuntimeConfig, input: { repoAlias?: string; workspaceId?: string }, runner: CommandRunner = systemRunner): { cwd: string; workspaceId?: string } {
  const hasAlias = typeof input.repoAlias === "string" && input.repoAlias.length > 0;
  const hasWorkspace = typeof input.workspaceId === "string" && input.workspaceId.length > 0;
  if (hasAlias === hasWorkspace) throw new Error("specify exactly one repoAlias or workspaceId");
  if (hasAlias) {
    const path = config.repoAliases?.[input.repoAlias!];
    if (!path) throw new Error("unknown repository alias; call repo_list and do not guess");
    return { cwd: existingDirectory(path) };
  }
  const topology = herdrTopology(config, runner);
  const workspace = topology.workspaces.filter(item => item.workspaceId === input.workspaceId);
  if (workspace.length !== 1) throw new Error("workspace is not uniquely available");
  const cwds = new Set(topology.panes.filter(pane => pane.workspaceId === input.workspaceId).map(pane => pane.foregroundCwd || pane.cwd).filter((cwd): cwd is string => Boolean(cwd)).map(existingDirectory));
  if (cwds.size !== 1) throw new Error("workspace cwd is ambiguous; use a validated repo alias");
  return { cwd: [...cwds][0], workspaceId: input.workspaceId };
}

const COPILOT_LABEL = "Context Drop Copilot";
const AI_TAB_LABEL = "Context Drop AI";

interface HerdrTabCreated {
  result?: {
    tab?: { tab_id?: string };
    root_pane?: { pane_id?: string };
  };
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}

function parseWorkspace(output: string | undefined): { workspace?: string; tab: string; pane: string } {
  let response: HerdrWorkspaceCreated;
  try {
    response = JSON.parse(output ?? "") as HerdrWorkspaceCreated;
  } catch {
    throw new Error("herdr returned invalid workspace JSON");
  }
  const workspace = response.result?.workspace?.workspace_id;
  const tab = response.result?.tab?.tab_id;
  const pane = response.result?.root_pane?.pane_id;
  if (!tab || !pane) throw new Error("herdr response omitted required tab/pane IDs");
  return { workspace, tab, pane };
}

function parseTab(output: string | undefined): { tab: string; pane: string } {
  let response: HerdrTabCreated;
  try {
    response = JSON.parse(output ?? "") as HerdrTabCreated;
  } catch {
    throw new Error("herdr returned invalid tab JSON");
  }
  const tab = response.result?.tab?.tab_id;
  const pane = response.result?.root_pane?.pane_id;
  if (!tab || !pane) throw new Error("herdr response omitted required tab/pane IDs");
  return { tab, pane };
}

function discoverWorkspaces(config: RuntimeConfig, session: string, runner: CommandRunner): Array<{ id: string; label: string; focused: boolean }> {
  const herdr = config.herdrPath || "herdr";
  const listed = runner.run(herdr, ["--session", session, "workspace", "list"]);
  if (listed.status !== 0) throw new Error(`herdr workspace discovery failed: ${listed.stderr || "unknown error"}`);
  let response: HerdrWorkspaceList;
  try {
    response = JSON.parse(listed.stdout ?? "") as HerdrWorkspaceList;
  } catch {
    throw new Error("herdr workspace discovery returned invalid JSON");
  }
  const workspaces = response.result?.workspaces;
  if (!Array.isArray(workspaces)) throw new Error("herdr workspace discovery omitted the workspace list");
  if (workspaces.some(workspace => typeof workspace !== "object" || workspace === null || typeof workspace.label !== "string" || typeof workspace.workspace_id !== "string" || !workspace.workspace_id || typeof workspace.focused !== "boolean")) {
    throw new Error("herdr workspace discovery returned malformed workspace data");
  }
  return workspaces.map(workspace => ({ id: workspace.workspace_id!, label: workspace.label!, focused: workspace.focused! }));
}

function createTab(herdr: string, session: string, workspace: string, repo: string, label: string, runner: CommandRunner): WorkspaceLocation {
  const created = runner.run(herdr, ["--session", session, "tab", "create", "--workspace", workspace, "--cwd", repo, "--label", label, "--no-focus"]);
  if (created.status !== 0) throw new Error(`herdr tab launch failed: ${created.stderr || "unknown error"}`);
  return { workspace, ...parseTab(created.stdout), createdFreshWorkspace: false };
}

function createWorkspace(herdr: string, session: string, repo: string, label: string, runner: CommandRunner): WorkspaceLocation {
  const created = runner.run(herdr, ["--session", session, "workspace", "create", "--cwd", repo, "--label", label, "--no-focus"]);
  if (created.status !== 0) throw new Error(`herdr workspace launch failed: ${created.stderr || "unknown error"}`);
  const location = parseWorkspace(created.stdout);
  if (!location.workspace) throw new Error("herdr workspace response omitted workspace ID");
  return { workspace: location.workspace, tab: location.tab, pane: location.pane, createdFreshWorkspace: true };
}

function humanCopilotLocation(config: RuntimeConfig, session: string, repo: string, runner: CommandRunner): WorkspaceLocation {
  const herdr = config.herdrPath || "herdr";
  const workspaces = discoverWorkspaces(config, session, runner);
  const focused = workspaces.filter(workspace => workspace.focused);
  if (focused.length > 1) throw new Error("herdr workspace discovery is ambiguous: multiple workspaces are focused");
  if (focused.length === 1) return createTab(herdr, session, focused[0].id, repo, COPILOT_LABEL, runner);
  const fallback = workspaces.filter(workspace => workspace.label === COPILOT_LABEL);
  if (fallback.length > 1) throw new Error(`herdr copilot workspace discovery is ambiguous: multiple workspaces are labeled ${COPILOT_LABEL}`);
  if (fallback.length === 1) return createTab(herdr, session, fallback[0].id, repo, COPILOT_LABEL, runner);
  return createWorkspace(herdr, session, repo, COPILOT_LABEL, runner);
}

function fullAILocation(config: RuntimeConfig, session: string, repo: string, runner: CommandRunner): WorkspaceLocation {
  const herdr = config.herdrPath || "herdr";
  const label = config.fullAIHerdrWorkspaceLabel || "ContextDropManaged";
  const existing = discoverWorkspaces(config, session, runner).filter(workspace => workspace.label === label);
  if (existing.length > 1) throw new Error(`herdr managed workspace discovery is ambiguous: multiple workspaces are labeled ${label}`);
  if (existing.length === 1) return createTab(herdr, session, existing[0].id, repo, AI_TAB_LABEL, runner);

  const location = createWorkspace(herdr, session, repo, label, runner);
  const renamed = runner.run(herdr, ["--session", session, "tab", "rename", location.tab, AI_TAB_LABEL]);
  if (renamed.status !== 0) {
    runner.run(herdr, ["--session", session, "workspace", "close", location.workspace]);
    throw new Error(`herdr managed tab rename failed: ${renamed.stderr || "unknown error"}`);
  }
  const afterCreate = discoverWorkspaces(config, session, runner).filter(workspace => workspace.label === label);
  if (afterCreate.length === 1 && afterCreate[0].id === location.workspace) return location;

  if (!afterCreate.some(workspace => workspace.id === location.workspace)) throw new Error("herdr managed workspace creation could not be proven from workspace discovery");
  const closed = runner.run(herdr, ["--session", session, "workspace", "close", location.workspace]);
  if (closed.status !== 0) throw new Error(`herdr managed workspace race cleanup failed: ${closed.stderr || "unknown error"}`);
  const survivor = discoverWorkspaces(config, session, runner).filter(workspace => workspace.label === label);
  if (survivor.length !== 1) throw new Error("herdr managed workspace creation could not be uniquely proven after race cleanup");
  return createTab(herdr, session, survivor[0].id, repo, AI_TAB_LABEL, runner);
}

// undefined means Herdr itself is unavailable, so callers must fail safe and
// preserve the task rather than treating every pane as gone.
export function paneAlive(config: RuntimeConfig, run: RunRecord, runner: CommandRunner = systemRunner): boolean | undefined {
  const herdr = config.herdrPath || "herdr";
  if (run.backend !== "herdr" || !run.herdrSession || !run.herdrPane) return undefined;
  const pane = runner.run(herdr, ["--session", run.herdrSession, "pane", "get", run.herdrPane]);
  if (pane.status === 0) return true;
  const reachable = runner.run(herdr, ["--session", run.herdrSession, "pane", "list"]);
  return reachable.status === 0 ? false : undefined;
}

export function closeHerdrWorker(config: RuntimeConfig, run: RunRecord, runner: CommandRunner = systemRunner): boolean {
  const alive = paneAlive(config, run, runner);
  if (alive === false) return true;
  if (alive !== true) return false;
  const herdr = config.herdrPath || "herdr";
  return runner.run(herdr, ["--session", run.herdrSession!, "pane", "close", run.herdrPane!]).status === 0;
}

export function continueInHerdr(config: RuntimeConfig, run: RunRecord, message: string, runner: CommandRunner = systemRunner): void {
  const herdr = config.herdrPath || "herdr";
  if (run.backend !== "herdr" || !run.herdrSession || !run.herdrPane) throw new Error("task has no persisted Herdr pane");
  const pane = runner.run(herdr, ["--session", run.herdrSession, "pane", "get", run.herdrPane]);
  if (pane.status !== 0) throw new Error(`delegated task pane is no longer available: ${pane.stderr || "pane not found"}`);
  const sent = runner.run(herdr, ["--session", run.herdrSession, "pane", "send-text", run.herdrPane, message]);
  if (sent.status !== 0) throw new Error(`delegated task follow-up was not sent: ${sent.stderr || "send-text failed"}`);
  const entered = runner.run(herdr, ["--session", run.herdrSession, "pane", "send-keys", run.herdrPane, "Enter"]);
  if (entered.status !== 0) throw new LaunchOutcomeUnknownError(`delegated task follow-up outcome is unknown: ${entered.stderr || "Enter failed"}`);
}

/** Continue any live Herdr agent, including agents not launched by Context Drop. */
export function continueLiveHerdr(config: RuntimeConfig, session: string, paneId: string, followUp: string, runner: CommandRunner = systemRunner): void {
  const herdr = config.herdrPath || "herdr";
  if (!/^w[^:]+:p[^:]+$/.test(paneId)) throw new Error("invalid Herdr pane ID");
  const listed = runner.run(herdr, ["--session", session, "agent", "list"]);
  if (listed.status !== 0) throw new Error(`live Herdr status is unavailable: ${listed.stderr || "agent list failed"}`);
  let response: { result?: { agents?: Array<{ pane_id?: string }> } };
  try { response = JSON.parse(listed.stdout ?? "") as typeof response; } catch { throw new Error("live Herdr status returned invalid JSON"); }
  if (!response.result?.agents?.some(agent => agent.pane_id === paneId)) throw new Error("Herdr pane is no longer available");
  const sent = runner.run(herdr, ["--session", session, "agent", "prompt", paneId, followUp]);
  if (sent.status !== 0) throw new Error(`delegated task follow-up was not sent: ${sent.stderr || "agent prompt failed"}`);
}

export function waitForHerdrAgent(config: RuntimeConfig, paneId: string, timeoutMs = 15_000, pollMs = 250, runner: CommandRunner = systemRunner): void {
  const deadline = Date.now() + timeoutMs;
  let reachable = false;
  let lastError = "";
  for (;;) {
    try {
      const agent = listHerdrAgents(config, runner).find(item => item.paneId === paneId);
      reachable = true;
      if (agent) return;
    } catch (err) {
      lastError = err instanceof Error ? err.message : "Herdr status unavailable";
    }
    const remaining = deadline - Date.now();
    if (remaining <= 0) break;
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, Math.min(pollMs, remaining));
  }
  if (!reachable) throw new LaunchOutcomeUnknownError(`herdr agent registration outcome is unknown: ${lastError || "agent list unavailable"}`);
  throw new Error(`herdr agent did not register in pane ${paneId} within ${timeoutMs}ms`);
}

export function cleanupHerdrLaunch(config: RuntimeConfig, run: RunRecord, runner: CommandRunner = systemRunner): boolean {
  if (!run.herdrSession || !run.herdrPane) return false;
  return runner.run(config.herdrPath || "herdr", ["--session", run.herdrSession, "pane", "close", run.herdrPane]).status === 0;
}

export function launchInHerdr(config: RuntimeConfig, request: LaunchRequest, id: string, runner: CommandRunner = systemRunner): RunRecord {
  const { name, runDir, argv, environment } = prepareLaunch(config, request, id);
  const herdr = config.herdrPath || "herdr";
  const lane = request.lane ?? (request.workspaceId ? "human_copilot" : "full_ai");
  const session = config.herdrSession || "default";
  let location: WorkspaceLocation;
  if (lane === "full_ai") {
    location = fullAILocation(config, session, request.repo, runner);
  } else if (request.workspaceId) {
    location = createTab(herdr, session, request.workspaceId, request.repo, COPILOT_LABEL, runner);
  } else {
    location = humanCopilotLocation(config, session, request.repo, runner);
  }

  const launcher = join(runDir, "launch.sh");
  const env = Object.entries(environment).map(([key, value]) => `${key}=${shellQuote(value)}`).join(" ");
  writeFileSync(launcher, `#!/bin/sh\nexec ${env ? `env ${env} ` : ""}${argv.map(shellQuote).join(" ")}\n`, { mode: 0o700 });
  chmodSync(launcher, 0o700);
  const start = runner.run(herdr, ["--session", session, "pane", "run", location.pane, shellQuote(launcher)]);
  if (start.status !== 0) {
    if (start.status === null) throw new LaunchOutcomeUnknownError(`herdr agent launch outcome is unknown: ${start.stderr || "command outcome unavailable"}`);
    // The pane/tab was created by this request and pane run definitively failed.
    // Close only that exact owned pane; never close a reused workspace.
    runner.run(herdr, ["--session", session, "pane", "close", location.pane]);
    if (location.createdFreshWorkspace) runner.run(herdr, ["--session", session, "workspace", "close", location.workspace]);
    throw new Error(`herdr agent launch failed: ${start.stderr || `exit status ${start.status}`}`);
  }

  return {
    id,
    name,
    agent: request.agent,
    repo: request.repo,
    backend: "herdr",
    herdrSession: session,
    herdrWorkspace: location.workspace,
    herdrTab: location.tab,
    herdrPane: location.pane,
    lane,
    finalMarker: request.finalMarker,
    finalOutputPath: request.finalOutputPath,
    status: "running",
    createdAt: new Date().toISOString(),
  };
}
