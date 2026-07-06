# Integrations

relaymux works without message adapters. The core integration point is local: the daemon binds to loopback and accepts authenticated requests from the relaymux CLI helpers.

## Local API And Agent Updates

`relaymux ask` and `relaymux notify --reply-mode ...` are CLI wrappers over the local API. The update endpoints are local callbacks for agents and helper scripts; they are not public webhooks.

Default endpoints:

| Endpoint | Purpose |
| --- | --- |
| `GET /health` | Returns daemon, queue, and local API status. |
| `POST /request` | Submits a terminal/local request to the orchestrator. |
| `POST /message` | Submits a subagent completion or update. |
| `POST /agent-message` | Alias for `POST /message`. |

Requests to `POST` endpoints require `Authorization: Bearer <token>`, where the token is stored in `~/.relaymux/state/webhook-token` by default.

Ask the orchestrator from a terminal:

```bash
relaymux ask "Open an agent in ~/code/my-app to inspect the failing test."
```

`relaymux ask` requires the relaymux daemon to be running. `relaymux setup` installs the daemon as a per-user LaunchAgent on macOS or a systemd user service on Linux, and `relaymux restart-launch-agent` reloads it after config changes (the command name is kept for CLI compatibility).

Every local API, adapter, and scheduled request is wrapped with relaymux's built-in orchestration baseline unless `orchestrator.defaultSystemPrompt` is set to `false`. relaymux then appends `<relaymux home>/AGENTS.md` when present, or `orchestrator.systemPromptFile` when configured.

Those defaults bias the orchestrator toward launching visible relaymux subagents for repo changes, debugging, research, CI, docs, validation, and other work that may take more than about 10 seconds.

Send a local run-log completion from a delegated agent. Local runs still record automatic `started` and `completed` events even without the daemon:

```bash
relaymux notify \
  --from fix-api \
  --message "Finished: fixed the API bug. Validation: npm test passed."
```

Use a stable `--idempotency-key` for one logical update so retries do not send duplicates. Keys are remembered in the daemon state under `~/.relaymux/state`.

For disposable tmux worker tabs, add `--suicide` to `relaymux notify`. relaymux sends or queues the notification first, then closes only the current tmux window. If notification fails, or if the command is not running inside tmux, relaymux leaves windows alone.

## Reply Modes

| `replyMode` | Behavior |
| --- | --- |
| `none` | The daemon passes the update to the orchestrator, but no adapter message is sent. |
| `imessage` | The daemon passes the update to the orchestrator, then sends the orchestrator's user-visible reply through the optional iMessage/SMS adapter. |
| `telegram` | The daemon passes the update to the orchestrator, then sends the orchestrator's user-visible reply through the optional Telegram adapter. |

Use `--reply-mode imessage` or `--reply-mode telegram` only when that adapter is configured and you want a user-visible update.
Completion updates always pass through the orchestrator first so it can observe, compress, and decide what user-visible adapter update to send.

## Scheduled Prompts

Schedule a recurring local prompt when you want the orchestrator asked on a clock, such as a weekday morning check-in:

```bash
relaymux schedule add \
  --name weekday-checkin \
  --cron "0 9 * * 1-5" \
  --reply-mode imessage \
  --prompt "Check the active agent runs and send me a concise status."
```

Scheduled prompts are local OS jobs. The default `auto` scheduler uses macOS launchd on macOS and cron elsewhere, including Linux. Each job runs `relaymux ask --no-wait` on the schedule; relaymux does not create a hidden cloud scheduler or a durable in-process loop inside the daemon. The relaymux daemon must be running when the schedule fires, so run `relaymux restart-launch-agent` after setup if needed. On Linux, make sure `crontab` is available through cron/cronie. Use `--dry-run` to inspect the generated job before installing it, or pass `--scheduler launchd|cron` when you want a specific backend.

Use `--prompt-file prompt.txt` for longer prompts. relaymux copies the prompt into `~/.relaymux/state/schedules/<name>/prompt.txt`, stores schedule metadata beside it, and writes schedule logs under `~/.relaymux/logs/schedules`. Re-adding the same `--name` updates that schedule instead of creating a duplicate.

```bash
relaymux schedule list
relaymux schedule remove --name weekday-checkin
```

`--reply-mode none` keeps the request local and quiet. `--reply-mode imessage` or `--reply-mode telegram` sends the orchestrator's final reply through that configured adapter. Cron expressions use five fields: minute, hour, day of month, month, and day of week. When a schedule uses launchd, avoid expressions that constrain both day of month and day of week because launchd matches those fields differently from cron.

## Direct HTTP

For foreground debugging instead of the installed background service, run `relaymux daemon`. If you cannot use the CLI helper, you can call the local API directly:

```bash
TOKEN="$(cat ~/.relaymux/state/webhook-token)"
curl -sS -X POST http://127.0.0.1:47761/message \
  -H "authorization: Bearer $TOKEN" \
  -H "content-type: application/json" \
  -d '{"from":"build-agent","replyMode":"none","idempotencyKey":"build-agent:job-123:done","text":"Finished; tests passed."}'
```

## iMessage/SMS

The iMessage/SMS adapter is optional. It uses your configured `imsg` command for both inbound polling and outbound replies. relaymux shells out to the command you configure; it does not vendor `imsg` or talk to Messages.app directly.

Expected command shapes are `imsg chats --limit <n> --json`, `imsg history --chat-id <id> --limit <n> --json`, and `imsg send --chat-id <id> --text <text> --json`.

```bash
relaymux setup --imsg --chat-id <chat-id-or-phone-number>
relaymux status-launch-agent
relaymux status
```

`relaymux setup --imsg` creates or updates `~/.relaymux/config.json`, tries to discover recent `imsg` chats when `--chat-id` is omitted, installs/restarts the background service unless `--no-launch-agent` is passed, and prints next steps. Re-running `relaymux init --imsg` or `relaymux setup --imsg` adds or updates the adapter on the existing config; `--force` is only for replacing the whole config.

Inbound iMessage/SMS access is allowlisted by the configured chat id. Receive commands must include the `{chatId}` template so polling is scoped to `config.integrations.imessage.chatId`. relaymux also inspects parsed command output: if a message exposes a chat, conversation, or thread id, the id must exactly match the configured chat id or the message is ignored before it reaches the orchestrator. Legacy command output that has no explicit chat id is accepted only under the scoped `{chatId}` receive command.

After setup, text the configured chat with a small request. Use a chat where your request appears as an incoming message to the Mac's Messages account; messages marked by Messages as sent by that Mac are ignored so relaymux does not respond to its own replies.

Manual user-visible completion:

```bash
relaymux notify \
  --from fix-api \
  --reply-mode imessage \
  --idempotency-key fix-api-20260614-done \
  --message "Finished: fixed the API bug. Validation: npm test passed."
```

## Telegram

The Telegram adapter is optional outbound notification support through `sendMessage`. It does not poll Telegram for inbound messages.

Create a bot with BotFather, get a chat id for the chat you want to notify, and store the token outside the public config. A token file works well with the background service because it does not depend on your interactive shell environment:

```bash
mkdir -p ~/.relaymux/secrets
printf '%s\n' '<telegram-bot-token>' > ~/.relaymux/secrets/telegram-bot-token
chmod 600 ~/.relaymux/secrets/telegram-bot-token

relaymux setup --telegram \
  --telegram-chat-id <telegram-chat-id> \
  --telegram-bot-token-file ~/.relaymux/secrets/telegram-bot-token
relaymux status-launch-agent
relaymux status
```

You can also use an environment variable instead of a file:

```json
{
  "integrations": {
    "telegram": {
      "enabled": true,
      "chatId": "<telegram-chat-id>",
      "botTokenEnv": "TELEGRAM_BOT_TOKEN",
      "parseMode": ""
    }
  }
}
```

Manual Telegram completion:

```bash
relaymux notify \
  --from fix-api \
  --reply-mode telegram \
  --idempotency-key fix-api-20260614-done \
  --message "Finished: fixed the API bug. Validation: npm test passed."
```
