# Context Drop

Context Drop is a small, local-first orchestration system for delegating work to coding agents from a private conversation. Its daemon keeps the orchestrator alive, supervises the local runtime, starts visible Herdr or tmux workers, delivers worker updates, runs schedules, and can serve a trusted iMessage chat on macOS.

The public CLI deliberately stays small:

```text
context-drop upload
context-drop report
context-drop schedule
context-drop daemon
context-drop version
```

The optional hosted component is only a temporary file store. It has no accounts, machine graph, remote command execution, or shared task state.

## What it does

- **Message orchestration:** an explicitly configured iMessage chat talks to one persistent local orchestrator.
- **Agent delegation:** the orchestrator can list live panes, delegate a fully managed task, or continue an exact live pane.
- **Natural-language reports:** managed workers run `context-drop report "message"`; scoped credentials route the message back to the owning conversation.
- **Schedules:** durable local schedules launch managed agent work through the daemon.
- **Temporary uploads:** authenticated uploads produce opaque, expiring public links.

Telegram is not implemented in this release. The daemon's current messaging adapter is iMessage.

## Install

Install a published macOS or Linux release:

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/install.sh | bash
context-drop version
```

The installer verifies the release checksum and installs the binary plus its Node runtime assets. Node.js 20+ is required by the local runtime. Herdr is the default worker backend; tmux is also supported.

Build from source with Go 1.26.2+, Node.js 20+, and npm:

```sh
git clone https://github.com/mupt-ai/context-drop.git
cd context-drop
make runtime-install
make install
```

## Quick start

Start the local orchestration core as a per-user service:

```sh
context-drop daemon install
context-drop daemon status
```

On first start, the daemon creates a private loopback runtime configuration, detects installed `pi`, `codex`, and `claude` CLIs, and creates local credentials under the Context Drop home directory. Agent CLIs must already be installed and authenticated. Use `context-drop daemon logs` if startup fails.

### Configure iMessage (macOS)

The adapter requires [`imsg`](https://github.com/steipete/imsg), an exact private chat ID, and a responder command. Messaging configuration is daemon-owned rather than a public CLI workflow. Provision the private `imessage/config.json` described in [Configuration](docs/configuration.md), then restart:

```sh
context-drop daemon restart
context-drop daemon status
```

The daemon ignores prior history during initial sync, durably deduplicates later messages, and never gives workers iMessage credentials. The persistent orchestrator exposes managed task controls plus constrained Herdr topology/read/status and validated-repository launch tools; every prompt and launch still crosses the managed safety/reporting boundary.

## Worker reports

A fully managed worker receives a task-scoped report URL, capability, and run ID in its launch environment. It reports plain language:

```sh
context-drop report "I reproduced the failure and am checking the parser now."
printf '%s\n' 'Finished: fixed the parser and all tests pass.' | context-drop report
```

There is no required status taxonomy. The orchestrator decides whether a report calls for a reply, follow-up, clarification, or no interruption. A report capability cannot upload files or control the daemon.

## Temporary uploads

Uploads require a dedicated bearer token. Configure it in the environment:

```sh
export CONTEXT_DROP_ENDPOINT=https://contextdrop.dev
export CONTEXT_DROP_UPLOAD_TOKEN='your-upload-token'
context-drop upload --ttl 1h ./screenshot.png
```

The command prints an opaque public URL. Anyone with the URL can read the file until expiry, so do not upload secrets. Clipboard image upload is opt-in:

```sh
context-drop upload --clipboard --ttl 15m
```

Self-host the basic file service with a separate upload token; see [Server](docs/server.md).

## Schedules

Schedules are private local state and require the daemon/runtime configuration:

```sh
context-drop schedule add \
  --name test-watch \
  --agent pi \
  --repo "$HOME/code/project" \
  --prompt "Inspect current test failures and report what you find" \
  --every 1h \
  --notify

context-drop schedule list
context-drop schedule run test-watch
context-drop schedule remove test-watch
```

Calendar schedules use `--cron` with `--timezone`. Each occurrence launches a fresh local agent task; missed intervals are bounded rather than replayed as an unlimited backlog.

## Daemon management

```sh
context-drop daemon status
context-drop daemon restart
context-drop daemon logs --lines 200
```

`install`, `uninstall`, `start`, `stop`, and foreground `run` remain daemon-administration subcommands. The runtime binds only to loopback and authenticates every control request with private local credentials.

## Security

- Messaging credentials stay in the daemon; workers receive only scoped report capabilities.
- Upload authentication is separate from runtime and reporting credentials.
- Task text and worker reports are untrusted claims, not authorization for payments, account recovery, or changed terms.
- Public upload links are unguessable bearer URLs with enforced TTL and size limits.
- Local agents run with the local user's permissions. Preserve unrelated Herdr workspaces and tmux panes.

See [Security](docs/security.md) and [Architecture](docs/architecture.md).

## Documentation

- [CLI reference](docs/cli.md)
- [Configuration](docs/configuration.md)
- [Local daemon and schedules](docs/local-launch.md)
- [Server and self-hosting](docs/server.md)
- [Troubleshooting](docs/troubleshooting.md)

## Development

```sh
make test
make validate
```

## License

[MIT](LICENSE)
