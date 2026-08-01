# relaymux

[![CI](https://github.com/mupt-ai/relaymux/actions/workflows/test.yml/badge.svg)](https://github.com/mupt-ai/relaymux/actions/workflows/test.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Coordinate local coding-agent CLIs in visible tmux windows, with an optional local daemon for requests, schedules, and iMessage or Telegram replies.

relaymux keeps the work on your machine. It starts coding-agent CLIs (Pi, Codex, Claude, or any command you configure) as normal local processes, gives each run its own tmux window, and records run state in an SQLite store. The agents retain the same local permissions they would have if you launched them yourself.

Two concepts are worth distinguishing early:
- **Agents** are the coding-agent CLIs (Pi, Codex, Claude) that relaymux launches into tmux windows to do work.
- **The orchestrator** is a separate local CLI that handles free-form requests from `relaymux ask`, scheduled prompts, or message adapters. It decides whether to answer inline or delegate to an agent with `relaymux launch`. By default the orchestrator is Pi when Pi is installed on PATH; otherwise setup writes a placeholder orchestrator that prints a setup reminder.
- **The daemon** is a background process (LaunchAgent on macOS, systemd user service on Linux) that serves the local API, polls adapters, and routes notifications. It is optional for direct launches.
- **Message adapters** (Telegram, iMessage/SMS) are optional inbound/outbound bridges between the daemon and a remote chat.

## Quick start

You need macOS or Linux, Node.js 20+, npm, tmux, and an installed and authenticated agent CLI.

Install relaymux from the current `main` branch:

```bash
curl -fsSL https://raw.githubusercontent.com/mupt-ai/relaymux/main/install.sh | bash
```

The installer downloads `main` and builds locally, then writes the app under `~/.local/lib/relaymux`. If `~/.local/bin` is not already on `PATH`, follow the instruction printed by the installer. To pin an installation, clone the repository, check out the desired commit, and run `./install.sh` from that checkout.

The default agent identifiers—`pi`, `codex`, `claude`—expect the corresponding CLI on your PATH at launch time.

Launch a first agent directly—no daemon or message adapter is required:

```bash
relaymux launch \
  --repo ~/code/my-app \
  --agent pi \
  --name inspect-tests \
  --prompt "Inspect the failing tests and summarize what is broken."
```

Then inspect the run or attach to it:

```bash
relaymux status
tmux attach -t agents
```

Detach from tmux with `Ctrl-b d`. Terminal disconnection does not stop the run; machine sleep, shutdown, or loss of power does.

## Why tmux?

A relaymux run is an ordinary agent process in its own tmux **window**—the tab-like unit shown by tmux and many terminal apps. That gives you a workspace you can inspect and control with familiar terminal tools:

- attach to watch output or type into an interactive agent;
- interrupt a process with `Ctrl-C`;
- keep multiple agents visible in one shared session;
- reconnect after closing a terminal or losing an SSH connection.

relaymux creates the `agents` session by default. A directly launched agent gets its own tmux window. Scheduled (recurring) launches reuse one persistent window per schedule: each tick replaces the previous run in the same schedule-named tab instead of accumulating a new tab, and superseded runs are marked reaped. Pass `--no-reuse-window` to opt out for a scheduled launch. relaymux never launches agents in hidden panes or a remote cloud worker.

## Add a local orchestrator

A directly launched agent performs one delegated task. The **orchestrator** handles requests submitted through `relaymux ask`, scheduled prompts, or optional message adapters. It is another local CLI command—Pi by default when Pi is installed—that can answer small requests itself or call `relaymux launch` for longer work.

Run setup to write `~/.relaymux/config.json`, install the per-user daemon, and check the local dependencies:

```bash
relaymux setup
relaymux doctor
relaymux status-launch-agent
```

The daemon binds to `127.0.0.1` and authenticates local API calls with a token stored under `~/.relaymux/state`. macOS uses a launchd LaunchAgent; Linux uses a systemd user service.

Ask the configured orchestrator from the same machine:

```bash
relaymux ask "Open an agent in ~/code/my-app to inspect the failing test."
```

If setup reports a placeholder orchestrator, install Pi or edit `orchestrator.command` in `~/.relaymux/config.json`, then run `relaymux restart-launch-agent`.

## Remote control with Telegram

Telegram setup scopes inbound polling and outbound replies to one configured chat ID. Create a bot with [BotFather](https://t.me/BotFather), save the token in a private file, and start setup:

```bash
mkdir -p ~/.relaymux/secrets
read -rsp "Telegram bot token: " TOKEN; echo
printf '%s\n' "$TOKEN" > ~/.relaymux/secrets/telegram-bot-token
unset TOKEN
chmod 600 ~/.relaymux/secrets/telegram-bot-token

relaymux setup --telegram \
  --telegram-bot-token-file ~/.relaymux/secrets/telegram-bot-token
```

When prompted, open the bot and send `/start`. relaymux discovers that chat ID, configures inbound polling, installs or restarts the daemon, and uses an installed Pi CLI as the orchestrator when one is available.

Verify the setup:

```bash
relaymux doctor
relaymux status
```

Now send the bot a request such as:

```text
Open an agent in ~/code/my-app and inspect the failing tests.
```

The local orchestrator decides whether to answer inline or launch a tmux worker. A delegated worker reports progress with `relaymux notify`; the orchestrator turns that update into the user-visible Telegram reply.

For iMessage/SMS setup on macOS, see [Message integrations](docs/integrations.md#imessagesms). It requires a separately installed and configured `imsg` command.

## Launch and completion

Use a prompt file for longer delegated tasks:

```bash
relaymux launch \
  --repo ~/code/my-app \
  --agent codex \
  --name fix-api \
  --prompt-file ./fix-api-prompt.md
```

A worker can send one idempotent completion update and close only its own disposable tmux window:

```bash
relaymux notify \
  --from fix-api \
  --reply-mode telegram \
  --idempotency-key fix-api-done \
  --message "Finished: fixed the API bug. Validation: npm test passed." \
  --suicide
```

relaymux queues or sends the update before closing the current window. If notification fails or the command is not running inside tmux, `--suicide` leaves tmux windows alone. For wrapper-level fallback notifications when an agent forgets to notify, use `relaymux launch --notify-on-exit failure|always`.

## Scheduled prompts

Schedules ask the local orchestrator through an OS job; they are not hosted jobs. The machine and relaymux daemon must be running when the schedule fires.

```bash
relaymux schedule add \
  --name weekday-status \
  --cron "0 9 * * 1-5" \
  --reply-mode telegram \
  --prompt "Check the active relaymux runs and send a concise status."

relaymux schedule list
relaymux schedule remove --name weekday-status
```

The expression uses five cron fields (minute, hour, day-of-month, month, day-of-week) in the system timezone. Pass `--scheduler launchd` or `--scheduler cron` to choose the backend explicitly; `auto` uses launchd on macOS and cron on Linux. Missed runs, overlapping runs, and machine-sleep gaps are not replayed.

When a scheduled prompt delegates work via `relaymux launch`, that launch reuses one persistent tmux window per schedule (named after the schedule) instead of opening a new tab every tick. The previous tick's window is replaced and its run record is marked reaped, so `relaymux status` shows one current run per schedule. One-off `relaymux launch` calls keep unique-tab behavior. Pass `--no-reuse-window` to opt out for a single scheduled launch, or `--schedule-name <name>` to force persistent-reuse on an ad-hoc launch.

## Configuration and operations

relaymux keeps local configuration, state, prompts, schedules, the SQLite store, and logs under `~/.relaymux/` by default. Agent and orchestrator entries are argv templates, so you can replace Pi, Codex, or Claude with another CLI.

- [Configuration](docs/configuration.md)—orchestrator vs. agents, command templates, prompt modes, tmux session modes
- [Integrations](docs/integrations.md)—local API, reply modes, schedules, iMessage/SMS, Telegram
- [Operations](docs/operations.md)—service management, safety, troubleshooting, uninstalling

## Safety and limits

relaymux is intended for one trusted user on one local machine. It is not a sandbox, multi-tenant service, durable distributed job system, hosted scheduler, hosted model provider, or web UI. A configured agent can read and change anything its local process account can access. Treat adapter access as remote access to that agent capability, keep bot tokens private, and configure only chats and agent commands you trust.

Piping a remote script into Bash (as the install.sh instruction does) has inherent trust implications. Review the installer before running it, or clone and build from source.

## Development

```bash
git clone https://github.com/mupt-ai/relaymux.git
cd relaymux
npm ci
npm run validate
```

## License

MIT. See [LICENSE](LICENSE).
