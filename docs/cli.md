# CLI reference

Context Drop has one public command: `context-drop`.

## Pairing and artifacts

- `context-drop init --machine-name NAME`
- `context-drop token create`
- `context-drop join TOKEN --machine-name NAME`
- `context-drop machines list`
- `context-drop upload PATH`
- `context-drop list`
- `context-drop pull [ID]`

## Handoffs

- `context-drop handoff create --to MACHINE --summary TEXT [--action TEXT] [--artifact PATH]`
- `context-drop inbox [--all] [--json]`
- `context-drop inspect ID [--json]`
- `context-drop accept ID [--json]` (always stages under private Context Drop state; `--into` is rejected)
- `context-drop reject ID`

## Local agents

- `context-drop daemon run|start|stop|restart|status|logs`
- `context-drop daemon install|uninstall`
- `context-drop daemon watchdog install|uninstall|status`
- `context-drop schedule add --name NAME --agent AGENT --repo ABSOLUTE_PATH --prompt TEXT (--every DURATION | --cron 'MIN HOUR DOM MONTH DOW' --timezone IANA_ZONE) [--notify] [--disabled] [--backend tmux|herdr]`
- `context-drop schedule list [--json]`
- `context-drop schedule run NAME`
- `context-drop schedule remove NAME`
- `context-drop runtime serve|status` (foreground/debug use; the daemon normally supervises it)
- `context-drop agent list [--json]`
- `context-drop agent configure --name NAME (--command-json '["exe","{prompt_file}"]' | --arg exe --arg '{prompt_file}') [--replace]`
- `context-drop launch --agent NAME --repo PATH --prompt TEXT [--name NAME] [--backend tmux|herdr] [--workspace HERDR_WORKSPACE_ID]`
- `context-drop run list [--json]`
- `context-drop run show ID`
- `context-drop imessage setup --chat-id ID [--recipient PHONE_OR_EMAIL] [--imsg-path ABSOLUTE_PATH] [--agent pi] [--poll 250ms]`
- `context-drop imessage status [--json]`
- `context-drop imessage latency [--last 50] [--minimum-sample 20] [--json]`
- `context-drop migrate relaymux inspect --home PATH --json`

`imessage setup` is noninteractive and never sends a message. Use `imsg chats --json` to discover the chat ID, then restart the daemon. `--poll` controls the preferred watch debounce and the legacy history-poll fallback interval. Advanced repeated `--responder-arg` flags replace the safe Pi preset and must include `{prompt_file}`; message/reply limits and command timeouts have explicit setup flags.

The latency report uses the latest successfully sent messages with parseable source timestamps. It reports nearest-rank distributions for source-to-send end-to-end latency and each recorded local stage, and only marks the built-in p50 <= 3s / p90 <= 8s target met when the requested minimum sample size is present.

`daemon install` writes and loads a per-user launchd service on macOS or systemd user unit on Linux. On macOS the optional watchdog checks service availability every 15 minutes; Linux uses the systemd restart policy and has no separate watchdog. `schedule --every` accepts Go durations and has a one-minute minimum. Alternatively, `--cron` accepts an exact five-field cron expression and requires an IANA `--timezone`; missed occurrences cause at most one launch on restart, followed by the next future wall-clock occurrence. Notifications are local OS notifications (or daemon log messages on non-macOS platforms).

`agent configure` preserves each argv element without shell parsing. The argv must contain exactly one `{prompt_file}` placeholder (including forms such as `@{prompt_file}`); existing registrations are refused unless `--replace` is explicit.

`migrate relaymux inspect` is read-only. Its JSON inventory reports redacted configuration presence, legacy schedules, launchd plist/load status, run/event line counts, data-path sizes, and unsupported migration blockers. It does not expose configured commands, tokens, recipients, or API keys and does not apply a migration.

There are no remote launch or automatic handoff dispatch commands. Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.
