# Typed schedules

Context Drop schedules are durable local workflows. Existing schedules are migrated in memory and on the next state write to `type=agent`, `overlap=skip`, and `missed_run_policy=latest`; their prompts and history are retained.

## Types

- `agent` preserves the managed worker/report workflow. Existing `--agent`, `--repo`, `--prompt`, and `--backend` flags continue to work.
- `command` executes an exact argv array directly, never through a shell. `--cwd` must be an existing absolute directory. Repeat `--command` once per argv element. This is appropriate for versioned digest/workflow scripts.
- `watch` polls an explicit backend pane (`--watch-pane`) or stable live task name (`--watch-target`). It never launches an agent and notifies only when terminal/blocking/missing state changes.

Examples:

```sh
context-drop schedule add --name check --type agent --agent pi --repo /absolute/repo --prompt 'check status' --every 15m
context-drop schedule add --name digest --type command --cwd /absolute/workflows --command /absolute/workflows/digest --command --deliver --every 1h --timeout 10m --retries 2
context-drop schedule add --name worker-watch --type watch --backend herdr --watch-pane workspace:pane --every 1m
```

`--command` is an exact argv entry: shell operators, substitutions, and redirections are ordinary text and are not interpreted.

For long agent workflows, keep the instructions in a versioned repository file and make the saved prompt a short directive to read and execute that file. For example, the personal digest schedule points at [`workflows/personal-digest/WORKFLOW.md`](../workflows/personal-digest/WORKFLOW.md) rather than embedding the full workflow in daemon state. Use `workflows/personal-digest/install-schedule.sh` to upsert or migrate the personal-digest schedule with the short prompt while preserving the existing cadence; the script does not launch work.

## Lifecycle and safety

Jobs use `queued`, `running`, `completed`, `failed`, `timed_out`, or `skipped`, with durable occurrence keys and start/finish timestamps. The default/latest missed-run policy coalesces missed intervals. `overlap=skip` prevents a new occurrence while a queued/running job exists and records the skipped occurrence. `queue` and `replace` are rejected until safe cancellation semantics exist.

Commands support context-enforced `--timeout`, up to ten `--retries`, consecutive failure tracking, and `--auto-pause-after`. Agent jobs remain running while their managed runtime task is live and are reconciled from live state.

Use `schedule pause NAME`, `schedule resume NAME`, `schedule run-now NAME` (legacy `schedule run` remains an alias), and `schedule list` for lifecycle visibility.
