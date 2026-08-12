# CLI reference

The public top-level command surface is intentionally limited to five commands.

## `context-drop upload [path]`

Upload one file to the configured TTL store. With no path, `--clipboard` uploads the current clipboard image.

Flags: `--endpoint`, `--ttl`, `--filename`, `--content-type`, `--clipboard`, `--no-clipboard`, and `--json`.

Uploads require `CONTEXT_DROP_UPLOAD_TOKEN` or `upload_token` in the private config file. The token is independent from daemon and worker-report credentials.

## `context-drop report [message]`

Send a natural-language update from a managed worker to its owning orchestrator. If the argument is omitted, the message is read from stdin. The daemon injects the required scoped environment values when launching a managed worker; this command is not a general messaging API.

## `context-drop schedule`

- `schedule add`: add or replace a durable interval (`--every`) or calendar (`--cron` and `--timezone`) schedule.
- `schedule list`: print schedules; `--json` also includes recent jobs.
- `schedule run NAME`: launch one occurrence immediately.
- `schedule remove NAME`: remove a schedule.

A schedule requires a configured agent, absolute repository path, prompt, and exactly one cadence.

## `context-drop daemon`

Use `status`, `restart`, and `logs` for routine administration. Service lifecycle commands (`install`, `uninstall`, `start`, `stop`) and foreground `run` are also available for installation and debugging.

## `context-drop version`

Print the binary version, source commit, and build date.
