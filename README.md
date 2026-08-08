# Context Drop

Context Drop is a CLI for moving files, messages, and deliberate handoffs between your machines—and for starting local coding agents where you can see them.

It keeps the boundary clear:

- **Remote:** pair machines, upload short-lived artifacts, and send inspectable handoffs.
- **Local:** accept artifacts into a private staging directory, then explicitly launch an agent in tmux or Herdr.

A handoff never executes code automatically. The hosted service stores and routes context; agent processes run only on the machine where you launch them.

## Install

Install the latest published release on macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/install.sh | bash
context-drop version
```

The installer detects your OS and architecture, downloads the matching GitHub release archive, verifies its SHA-256 checksum, and installs `context-drop` under `/usr/local/bin` when writable or `~/.local/bin` otherwise. If `~/.local/bin` is used, add it to your `PATH`.

To choose an installation directory explicitly:

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/install.sh | \
  CONTEXT_DROP_INSTALL_DIR="$HOME/.local/bin" bash
```

For development from source, use Go 1.26.2+, Node.js 20+, npm, and tmux:

```sh
git clone https://github.com/mupt-ai/context-drop.git
cd context-drop
make runtime-install
make install
```

## Quick start: hand off context

A **machine chain** is a group of paired machines that can exchange drops, messages, and handoffs through the default hosted relay at `https://contextdrop.dev`. No account or API key is required.

Create a chain on the first machine:

```sh
context-drop init --machine-name laptop
context-drop token create --ttl 5m
```

On the second machine, join with the one-time token printed by the previous command:

```sh
context-drop join TOKEN --machine-name desktop
```

Back on the first machine, create an inspectable handoff:

```sh
context-drop handoff create \
  --to desktop \
  --summary "Review this test failure; do not edit files" \
  --action "Return a proposed fix" \
  --artifact ./failure.log
```

On the recipient, `inbox` prints the handoff ID:

```sh
context-drop inbox
context-drop inspect HANDOFF_ID
context-drop accept HANDOFF_ID
```

A **handoff** bundles a summary, a requested action, and optional uploaded artifacts for one paired machine. `accept` verifies and downloads attachments under `~/.context-drop/staging/`, prints the exact new directory, and does not run or open anything.

For a lightweight message or file link instead, use `context-drop send --to desktop ...`; the recipient reads it with `context-drop messages list`. A **drop** is the short-lived uploaded file itself and can be downloaded with `context-drop pull DROP_ID`.

## Quick start: run a local agent

Initialization detects installed `pi`, `codex`, and `claude` coding-agent CLIs; each must already be installed and authenticated. Install and immediately load the per-user daemon service, then launch a visible run:

```sh
context-drop daemon install
context-drop launch \
  --agent pi \
  --repo "$HOME/code/project" \
  --prompt "Inspect the failing tests" \
  --name inspect-tests
```

The launch output includes the exact `tmux attach -t ...` command. The daemon is required for launches, schedules, and iMessage handling, but not for pairing or manual handoffs. Check configured agents and runs with:

```sh
context-drop daemon status
context-drop agent list
context-drop run list
context-drop run show RUN_ID
```

Herdr is optional. If installed and running, use it for a workspace instead:

```sh
context-drop launch --backend herdr --agent pi \
  --repo "$HOME/code/project" \
  --prompt "Inspect the failing tests" \
  --name inspect-tests
```

Missing Herdr does not prevent initialization, pairing, handoffs, or tmux launches; a Herdr launch will fail until its CLI is installed and its named session is running.

## Upload short-lived artifacts

Passing a file directly is shorthand for `context-drop upload`. This uploads it and prints its temporary URL:

```sh
context-drop --ttl 1h ./screenshot.png
```

List or download drops in your chain:

```sh
context-drop list
context-drop pull DROP_ID --output ./screenshot.png --force
```

Clipboard integration is opt-in. With a path it uploads the file and copies the resulting URL; without a path it uploads the current clipboard image:

```sh
context-drop --clipboard ./screenshot.png
context-drop --clipboard
```

## Explicit local schedules

Schedules are local, persist on disk, and create a fresh local agent run at each interval using the prompt captured when the schedule is created. They require the daemon:

```sh
context-drop schedule add \
  --name test-watch \
  --agent pi \
  --repo "$HOME/code/project" \
  --prompt "Inspect current test failures" \
  --every 1h \
  --notify
context-drop schedule list
```

Schedules are not created from incoming handoffs, and handoffs do not trigger remote execution.

## Optional iMessage/SMS adapter

On macOS, install and authorize [`imsg`](https://github.com/steipete/imsg), find the private chat ID, then configure one chat explicitly:

```sh
imsg chats --json
context-drop imessage setup --chat-id CHAT_ID --recipient PHONE_OR_EMAIL --agent pi
context-drop daemon restart
context-drop imessage status
context-drop imessage latency --last 50 --minimum-sample 20
```

Existing messages are marked seen during setup. Only later messages are passed to the configured coding-agent responder, which returns a text reply. The default Pi responder runs without tools, context files, extensions, or session persistence. This adapter is separate from machine handoffs.

## Security model

Pairing authorizes access to a machine chain; it does not make received content trusted. Artifact URLs are bearer links until they expire. Do not send secrets. Local agents run with the local user’s permissions, and inbound handoffs are never automatically accepted or executed.

## Inspect a legacy Relaymux installation

Before any cutover, create a redacted, read-only inventory:

```sh
context-drop migrate relaymux inspect --home "$HOME/.relaymux" --json
```

Inspection reports schedules, launchd presence/load state, legacy run/event counts, major data-path sizes, and unsupported parity blockers. It never applies changes or prints configured commands, tokens, chat recipients, or API keys. Context Drop does not yet claim full Relaymux `ask`/`notify`, worktree, steering, or historical-run import parity.

## Documentation

- [Getting started](docs/getting-started.md)
- [First handoff](docs/first-handoff.md)
- [CLI reference](docs/cli.md)
- [Local agent launches](docs/local-launch.md)
- [Configuration](docs/configuration.md)
- [Security](docs/security.md)
- [Troubleshooting](docs/troubleshooting.md)

## Development

```sh
make test
make validate
make runtime-install
```

## License

[MIT](LICENSE)
