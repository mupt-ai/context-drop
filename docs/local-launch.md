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

The trusted conversation router exposes exactly:

- `list_tasks`: query the selected live backend and return public pane IDs, agent, optional name, status, selection state, and whether Context Drop fully manages the task.
- `delegate_task`: start a fully managed task using configured defaults and an optional configured agent/name.
- `continue_task`: send a follow-up to an exact live Herdr (`wX:pY`) or tmux (`%N`) pane.

Continuation is available for managed and unmanaged live panes. Pane IDs must come from live status or a trusted report and must never be guessed.

Managed Herdr work uses new tabs in the reusable `ContextDropManaged` workspace in session `default`. Context Drop must not close or disturb unrelated workspaces, tabs, or panes.

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
