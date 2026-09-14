# Context Drop

Context Drop is a small, local-first orchestration system for delegating work to coding agents from a private conversation. Its daemon keeps the orchestrator alive, supervises the local runtime, maintains four warm coding-agent workers (Pi, Codex, or Claude Code) in native Herdr tabs, delivers worker updates, runs schedules, and can serve a trusted iMessage chat on macOS.

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
- **Agent delegation:** the main orchestrator can text you and delegate to workers 1–4.
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

The installer verifies the release checksum and installs the binary plus its Node runtime assets. Node.js 20+ is required by the local runtime. Herdr hosts and drives the workers.

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

All four workers run one agent. Inspect or change it, then restart the daemon to apply:

```sh
context-drop config worker-agent
context-drop config worker-agent claude
context-drop daemon restart
```

### Configure iMessage (macOS)

The adapter requires [`imsg`](https://github.com/steipete/imsg), an exact private chat ID, and a responder command. Messaging configuration is daemon-owned rather than a public CLI workflow. Provision the private `imessage/config.json` described in [Configuration](docs/configuration.md), then restart:

```sh
context-drop daemon restart
context-drop daemon status
```

The daemon deduplicates incoming messages and owns all texting. The main orchestrator exposes only `delegate_to_worker(worker, prompt)`; its final response is texted to you. Four persistent agents of the configured kind share a durable task queue with schedules. New tasks fork the main conversation, including compaction, without starting another process or generating an extra summary.

## Worker reports

A fully managed worker receives a task-scoped report URL, capability, and run ID in its launch environment. It reports plain language:

```sh
context-drop report "I reproduced the failure and am checking the parser now."
printf '%s\n' 'The parser fix is in place; checking the remaining cases.' | context-drop report
```

Worker final answers are reported automatically. The main speaks naturally in the shared AGENTS.md style, asks questions directly, and keeps worker routing internal. Use `context-drop report --question "Your question"` when user input is required, then end the turn. The worker remains waiting; the user’s answer resumes the same task. A report capability cannot delegate, upload, or select a recipient.

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
  --repo "$HOME/code/project" \
  --prompt "Inspect current test failures and report what you find" \
  --every 1h

context-drop schedule list
context-drop schedule test-watch        # show a schedule's stored prompt
context-drop schedule test-watch "New prompt text"  # update only the prompt
context-drop schedule run test-watch
context-drop schedule remove test-watch
```

Calendar schedules use `--cron` with `--timezone`. Each occurrence queues work in the same four-worker pool; missed intervals are bounded rather than replayed as an unlimited backlog.

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
- Local agents run with the local user's permissions. Preserve unrelated Herdr workspaces and tabs.

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
