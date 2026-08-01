import { createCommandWindow, killWindowByTarget, listAgentWindows, setWindowMetadata } from "../tmux.js";
import { recordRun, reapScheduleRuns } from "../state.js";

import { prepareLaunchArtifacts } from "./artifacts.js";
import { scheduleWindowName } from "./names.js";
import { withScheduleLock } from "./lock.js";

export function launchTmuxAgent(request) {
  const sessionInfo = request.sessionInfo;
  if (!sessionInfo?.session) {
    throw new Error("tmux launch requires a resolved session");
  }

  const reuse = resolveScheduleReuse(request);
  const launchName = reuse ? reuse.windowName : request.name;
  const artifactsRequest = reuse ? { ...request, name: launchName } : request;
  const artifacts = prepareLaunchArtifacts(artifactsRequest, { writeFiles: !request.dryRun });

  if (request.dryRun) {
    request.io.stdout.write(`# tmux session: ${sessionInfo.session} (${sessionInfo.mode}; ${sessionInfo.source})\n`);
    if (reuse) {
      request.io.stdout.write(`# schedule: ${reuse.scheduleName} (reusing persistent window ${reuse.windowName})\n`);
    }
    if (request.worktreeAddArgs) {
      request.io.stdout.write(`worktree: ${request.quoteArgv(["git", ...request.worktreeAddArgs])}\n`);
    }
    request.io.stdout.write(`${artifacts.shellCommand}\n`);
    request.io.stdout.write("\n# wrapper script\n");
    request.io.stdout.write(`${artifacts.script}\n`);
    return { dryRun: true, ...artifacts };
  }

  if (request.printCommand) {
    request.io.stdout.write(`${artifacts.shellCommand}\n`);
  }

  const placeWindow = () => {
    if (reuse) {
      const priorWindows = listAgentWindows({ session: sessionInfo.session })
        .filter((window) => window.scheduleName === reuse.scheduleName || window.windowName === reuse.windowName);
      for (const window of priorWindows) {
        killWindowByTarget(window.target);
      }
      reapScheduleRuns(request.stateDir, reuse.scheduleName, { exceptRunId: request.runId });
    }

    const target = createCommandWindow({
      session: sessionInfo.session,
      name: launchName,
      cwd: request.workdir,
      shellCommand: artifacts.shellCommand,
    });

    const started = new Date().toISOString();
    setWindowMetadata(target.windowTarget, {
      relaymux: "1",
      relaymux_agent: request.agentName,
      relaymux_name: launchName,
      relaymux_repo: request.repo,
      relaymux_run_id: request.runId,
      relaymux_session: sessionInfo.session,
      relaymux_session_mode: sessionInfo.mode,
      relaymux_started: started,
      ...(reuse ? { relaymux_schedule: reuse.scheduleName } : {}),
    });
    recordRun(request.stateDir, {
      time: started,
      runId: request.runId,
      session: sessionInfo.session,
      sessionMode: sessionInfo.mode,
      sessionSource: sessionInfo.source,
      target: target.target,
      windowTarget: target.windowTarget,
      name: launchName,
      agent: request.agentName,
      repo: request.repo,
      workdir: request.workdir,
      promptFile: artifacts.promptFile,
      scriptFile: artifacts.scriptFile,
      command: artifacts.shellCommand,
      ...(reuse ? { scheduleName: reuse.scheduleName } : {}),
    });

    if (reuse) {
      request.io.stdout.write(`Reused schedule ${reuse.scheduleName} window ${reuse.windowName} for ${request.runId}\n`);
    }
    request.io.stdout.write(`Started ${launchName} in tmux session ${sessionInfo.session} tab ${target.windowTarget} (target ${target.target})\n`);
    request.io.stdout.write(`Run ID: ${request.runId}\n`);
    if (request.attach) {
      request.io.stdout.write(`Attach with: tmux attach -t ${sessionInfo.session}\n`);
    }
    return { target, ...artifacts };
  };

  if (reuse) {
    return withScheduleLock(
      { stateDir: request.stateDir, scheduleName: reuse.scheduleName, windowName: reuse.windowName },
      placeWindow,
    );
  }
  return placeWindow();
}

// Scheduled (recurring, named) launches reuse one persistent tmux window per
// schedule instead of accumulating a new tab every tick. Reuse is active when a
// schedule name is available (from --schedule-name or the RELAYMUX_SCHEDULE_NAME
// env the daemon sets for scheduled orchestrator runs) and the caller has not
// disabled it with --no-reuse-window. One-off `relaymux launch` calls keep the
// current per-launch tab behavior.
function resolveScheduleReuse(request) {
  const scheduleName = request.scheduleName || "";
  if (!scheduleName) return null;
  if (request.reuseWindow === false) return null;
  return { scheduleName, windowName: scheduleWindowName(scheduleName) };
}
