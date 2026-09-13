# Local daemon and schedules

```sh
context-drop daemon install
context-drop daemon status
context-drop daemon logs --lines 200
context-drop daemon restart
```

The daemon warms four native Pi workers. Herdr workers live in the configured managed workspace; tmux workers live in the configured session. Each slot keeps its pane for later tasks. Changing tasks switches the Pi conversation without restarting the agent. Unrelated panes are never adopted or controlled.

The main conversation's only tool is `delegate_to_worker(worker, prompt)`, with worker numbers 1–4. Final main responses are texted to the user. Workers have coding tools and report through Context Drop. They cannot create another delegation level.

```sh
context-drop report "Tests are passing; checking the migration."
context-drop report --question "Which repository should I use?"
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

New schedules contain prompts. Command records execute their argv directly in the daemon; watch records remain worker tasks. Per-schedule agent/backend selection and direct command retries are removed. Select Herdr or tmux for the pool in runtime configuration.

Existing old managed-task records are not adopted into the new pool. Finish old workers before installing this version; their old reporting capabilities do not authorize the new runtime. Native pool workers and their durable outboxes survive ordinary runtime restarts.
