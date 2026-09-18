# Local daemon and schedules

```sh
context-drop daemon install
context-drop daemon status
context-drop daemon logs --lines 200
context-drop daemon restart
```

The daemon warms four native workers running the configured agent (`context-drop config worker-agent`, applied on restart). They live in the configured managed Herdr workspace. Each slot keeps its tab for later tasks. Changing tasks resets the agent's conversation without restarting it. Unrelated panes are never adopted or controlled.

The main can use `delegate_to_worker` (workers 1–4), `list_workspaces`, and `delegate_to_workspace`. Final main responses are texted to the user. Workers have coding tools and report through Context Drop. For workspace tasks they coordinate a coding agent in the selected workspace; they cannot create additional pool workers.

```sh
context-drop report "Tests are passing; checking the migration."
context-drop report --question "Which repository should I use?"
context-drop report --final "Migration applied and verified."
```

Final worker answers are reported automatically. Do not duplicate them with an explicit report. Questions retain the worker's task until the main orchestrator delegates the user's answer to that worker.

```sh
context-drop schedule add --name test-watch \
  --repo "$HOME/code/project" \
  --prompt "Inspect current test failures and report the result" \
  --every 30m
context-drop schedule list
context-drop schedule test-watch
context-drop schedule test-watch "Updated task prompt"
context-drop schedule run test-watch
context-drop schedule pause test-watch
context-drop schedule resume test-watch
context-drop schedule remove test-watch
```

Calendar schedules use `--cron '0 9 * * 1-5' --timezone America/Los_Angeles`. Every agent occurrence claims the shared pool; if all four workers are occupied it waits. Timing is deterministic, overlap is skipped, and missed intervals are bounded. Schedules are not invented from conversation messages or reports.

New schedules contain prompts. Command records execute their argv directly in the daemon; watch records remain worker tasks. Per-schedule agent/backend selection and direct command retries are removed. Select the agent for the pool with `context-drop config worker-agent`.

Existing old managed-task records are not adopted into the new pool. Finish old workers before installing this version; their old reporting capabilities do not authorize the new runtime. Native pool workers and their durable outboxes survive ordinary runtime restarts.

## Existing Herdr workspaces

Requests such as “start a new task in dari-mono” can target an existing workspace. The main discovers workspace and pane IDs, then uses `delegate_to_workspace(worker, workspace, mode, prompt, paneId?, cwd?, newTask?)`. `mode: new` instructs the coordinator to create an isolated gwt worktree and a new, named tab. `mode: continue` requires an exact existing agent pane and preserves its conversation and worktree—no reset, restart, move, or close. Multiple matching workspaces/conversations require clarification; new tasks in multi-directory workspaces require a discovered cwd. Missing targets never silently fall back.

A pool worker still coordinates, monitors, and reports this work; this is not direct runtime-controlled dispatch into user-owned panes. The runtime persists the destination and validates discovery, while the coordinator performs the Herdr actions. Later messages to that worker retain the destination and reuse the same task tab. A changed destination requires a separate task. Report turns cannot initiate either kind of delegation. Schedules and unspecified work retain the default four-slot pool.
