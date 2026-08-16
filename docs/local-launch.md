# Local daemon, delegation, and schedules

The Context Drop daemon is the local orchestration core. It owns the persistent conversation responder, supervises a private loopback Node runtime, starts agent panes, observes managed-task lifecycle, delivers reports, and claims scheduled work durably.

## Service management

```sh
context-drop daemon install
context-drop daemon status
context-drop daemon logs --lines 200
context-drop daemon restart
```

On macOS the installer uses a per-user LaunchAgent. On Linux it uses a systemd user service. Foreground `context-drop daemon run` is available for debugging.

## Orchestrator task tools

The trusted conversation orchestrator exposes managed task controls and full read-only Herdr inspection:

- `list_tasks`: query every worker in the selected live backend and return public pane IDs, agent, optional name, status, selection state, and whether Context Drop fully manages the task.
- `delegate_task`: start a fully managed task using configured defaults and an optional configured agent/name.
- `continue_task` / `herdr_prompt`: send a follow-up through the same managed continuation boundary to an exact live pane. An authorized-sensitive worker cannot be continued.
- `herdr_overview` / `herdr_read`: inspect the full configured Herdr session without exposing raw credentials.
- `herdr_wait`: poll authoritative status client-side with a bounded timeout and cancellation; it never invokes a blocking Herdr wait subprocess and reports timeout separately from observed status.
- `repo_list` / `start_agent`: select only a validated alias or unambiguous live workspace cwd, then launch a fully managed, tracked worker with reporting, safety policy, and capacity enforcement.

Manage aliases without editing runtime JSON:

```sh
context-drop repo add context-drop /absolute/path/to/context-drop
context-drop repo list
context-drop repo remove context-drop
```

Alias paths are canonicalized and must already be absolute directories.

Continuation is available for every managed or unmanaged live pane, including agents currently marked `idle` or `done`. Pane IDs must come from live status or a trusted report and must never be guessed. Adopting an unmanaged or previously completed pane creates fresh managed tracking and scoped reporting before the prompt is sent.

Managed Herdr work uses new tabs in the reusable `ContextDropManaged` workspace in the configured `CONTEXT_DROP_HERDR_SESSION`; full-AI work never silently switches to another session. Workspace-targeted launches use a new copilot tab in that exact validated workspace. Context Drop must not close or disturb unrelated workspaces, tabs, or panes.

## Worker reports

Managed workers receive scoped reporting values in their launch environment and use:

```sh
context-drop report "Natural-language progress or result"
```

The message enters the owning orchestrator conversation. Worker-authored reports are complemented by daemon lifecycle events if a managed pane exits, crashes, or disappears. Reporting credentials cannot control the daemon or upload files.

## Schedules

```sh
context-drop schedule add --name test-watch \
  --agent pi --repo "$HOME/code/project" \
  --prompt "Inspect current test failures and report naturally" \
  --every 30m --notify
context-drop schedule list
context-drop schedule run test-watch
context-drop schedule remove test-watch
```

Calendar schedules use `--cron '0 9 * * 1-5' --timezone America/Los_Angeles`. Schedules persist a prompt snapshot and launch fully managed local work through the daemon. Claims and job outcomes are written durably so overlapping ticks do not launch the same occurrence twice.
