# Schedules

A schedule is a saved prompt and an explicit interval or calendar rule. The daemon owns timing; the main agent does not invent or run schedules itself.

```sh
context-drop schedule add --name check --repo /absolute/repo \
  --prompt 'Check the test results and report what changed.' --every 15m
context-drop schedule add --name morning --prompt 'Ask which task I want to prioritize.' \
  --cron '0 9 * * 1-5' --timezone America/Los_Angeles
context-drop schedule list
context-drop schedule pause check
context-drop schedule resume check
context-drop schedule run check
context-drop schedule remove check
```

`--repo` defaults to the current directory. `--prompt-file` snapshots a file instead of accepting inline text. `schedule NAME` shows the saved prompt; `schedule NAME "new prompt"` updates it without changing its cadence.

Each agent occurrence has a durable identity and enters the same queue as user-delegated work. Command schedules run their stored argv directly in the daemon, without a worker or orchestrator call. They retain overlap protection, timeouts, durable job status, and per-job output logs. Interrupted scripts are not replayed after a restart. There are exactly four native Codex workers across both sources. Busy workers cause queueing, not extra agent launches. An occurrence that could not be submitted stays queued and retries with the same identity, so a lost HTTP response cannot create duplicate work. Overlap is skipped and missed intervals are coalesced to the latest occurrence.

Final answers reach the main orchestrator automatically. For background maintenance, use `schedule add --silent` or `context-drop schedule NAME --silent` on an existing schedule. Routine progress and completion stay internal, without a main-model turn or a user message. Questions and failures still reach the user. Use `context-drop schedule NAME --silent=false` to restore routine messages. Results remain in runtime reports and job history.

A worker needing input uses `context-drop report --question "..."` and ends its turn. Its slot remains waiting until the main routes the user's answer back to it. A schedule waiting for an answer is still active and cannot overlap itself.

Legacy command and watch definitions remain readable and become worker prompts. New schedules have no separate command runner, pane watcher, agent selector, per-schedule backend, or direct execution retries. Herdr/tmux is chosen for the pool as a whole.

Job completion and report delivery are separate. `schedule list --json` includes both; a completed task may still have an undelivered report. Native workers keep report outboxes across daemon outages. Ambiguous iMessage sends are not automatically repeated.
