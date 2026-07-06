# relaymux

`relaymux` is a lightweight local metaharness for coding agents.

Telegram or iMessage can be the remote control / orchestrator; `tmux` tabs are where the actual agent work happens.

When relaymux launches an agent, it opens a visible `tmux` tab on your machine so you can attach, watch, interrupt, or debug the run like any normal terminal session.

## Requirements

You need Node.js 20+, npm, `tmux`, and a local agent CLI such as `pi`, `codex`, or `claude`.

SQLite support uses the system `sqlite3` CLI for `relaymux db` commands; normal launch, status, notify, and adapter commands do not require it.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mupt-ai/relaymux/main/install.sh | bash
```

## Quickstart

Create a bot with [BotFather](https://t.me/BotFather), copy the token, then run:

```bash
relaymux setup --telegram --telegram-bot-token '<telegram-bot-token>'
```

When prompted, open your bot in Telegram and send `/start`. relaymux will automatically start the background service.

Check it:

```bash
relaymux status
```

Send your Telegram bot a message like:

```text
Open an agent in ~/code/my-app and inspect the failing tests.
```

relaymux passes the message to your configured local orchestrator. If the orchestrator launches an agent, that agent runs in `tmux`; the final reply comes back through Telegram.

Manual notification test:

```bash
relaymux notify --from test --reply-mode telegram --message "hello from relaymux"
```

Disposable worker tabs can add `--suicide` to close only the current tmux window after the notification succeeds.

## tmux is the workspace

relaymux uses one shared `tmux` session named `agents` by default. Each launched agent gets its own tmux window. This is the core idea: Telegram or iMessage starts and receives updates from work, but tmux keeps that work visible and recoverable locally.

Attach any time:

```bash
tmux attach -t agents
```

Detach with `Ctrl-b d`. Closing Telegram or iMessage does not stop the tmux run.

## Launch an agent manually

```bash
relaymux launch \
  --repo ~/code/my-app \
  --agent pi \
  --name inspect-tests \
  --prompt "Inspect the failing tests and summarize what is broken."
```

Then attach with:

```bash
tmux attach -t agents
```
