import { chmodSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { LaunchRequest, RunRecord, RuntimeConfig } from "./types.js";
import { prepareLaunch, systemRunner, type CommandRunner } from "./launch.js";

interface HerdrWorkspaceCreated {
  result?: {
    workspace?: { workspace_id?: string };
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

export function launchInHerdr(config: RuntimeConfig, request: LaunchRequest, id: string, runner: CommandRunner = systemRunner): RunRecord {
  const { name, runDir, argv, environment } = prepareLaunch(config, request, id);
  const herdr = config.herdrPath || "herdr";
  const session = config.herdrSession || "default";
  const create = request.workspaceId
    ? runner.run(herdr, ["--session", session, "tab", "create", "--workspace", request.workspaceId, "--cwd", request.repo, "--label", name, "--no-focus"])
    : runner.run(herdr, ["--session", session, "workspace", "create", "--cwd", request.repo, "--label", name, "--no-focus"]);
  if (create.status !== 0) throw new Error(`herdr ${request.workspaceId ? "tab" : "workspace"} launch failed: ${create.stderr || "unknown error"}`);
  const location = parseWorkspace(create.stdout);
  const workspace = request.workspaceId ?? location.workspace;
  if (!workspace) throw new Error("herdr workspace response omitted workspace ID");

  const launcher = join(runDir, "launch.sh");
  const env = Object.entries(environment).map(([key, value]) => `${key}=${shellQuote(value)}`).join(" ");
  writeFileSync(launcher, `#!/bin/sh\nexec ${env ? `env ${env} ` : ""}${argv.map(shellQuote).join(" ")}\n`, { mode: 0o700 });
  chmodSync(launcher, 0o700);
  const start = runner.run(herdr, ["--session", session, "pane", "run", location.pane, shellQuote(launcher)]);
  if (start.status !== 0) {
    if (request.workspaceId) {
      runner.run(herdr, ["--session", session, "tab", "close", location.tab]);
    } else {
      runner.run(herdr, ["--session", session, "workspace", "close", workspace]);
    }
    throw new Error(`herdr agent launch failed: ${start.stderr || "unknown error"}`);
  }

  return {
    id,
    name,
    agent: request.agent,
    repo: request.repo,
    backend: "herdr",
    herdrSession: session,
    herdrWorkspace: workspace,
    herdrTab: location.tab,
    herdrPane: location.pane,
    status: "running",
    createdAt: new Date().toISOString(),
  };
}
