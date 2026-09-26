# Context Drop

Context Drop is two things:

- `cli/`: the `context-drop` command. It lets a chat assistant such as [OpenClaw](https://openclaw.ai) drive coding agents that run in [Herdr](https://herdr.dev) tabs, and it uploads files to a small TTL file store. The store's server lives here too.
- `ios/`: the Context Drop iPhone app (health, workouts, goals, and digests).

Messaging, scheduling, and memory belong to the assistant. Context Drop has no daemon.

## CLI

```text
context-drop agents                          list herdr agents and their status
context-drop new NAME PROMPT --workspace LABEL --cwd DIR [--agent claude|codex|pi] [--wait]
context-drop send TARGET TEXT [--force] [--wait]
context-drop wait TARGET
context-drop read TARGET
context-drop upload [path] [--ttl 1h]
context-drop version
```

`new` opens a tab in an existing herdr workspace, launches the agent through `dari`, names it, and submits the prompt. `send` continues an existing agent by name or pane ID, and refuses if it is busy unless you pass `--force`. `--wait` and `wait` block until the agent finishes its turn or stops to ask something, then print its output. That makes them easy to run as a background command that wakes the assistant when it returns. See [docs/cli.md](docs/cli.md).

Install a release:

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/cli/install.sh | bash
```

Or build from source with Go 1.26.2+:

```sh
cd cli
make install   # installs to ~/.local/bin
make test
```

`cli/jobs/` holds small standalone jobs that run from launchd: a Herdr activity journal, a tab auto-namer, and a health-dashboard alert cooldown. Each has its own README.

## Upload server

The hosted component is a temporary file store: one authenticated upload endpoint and opaque, expiring download links. See [docs/server.md](docs/server.md).

## iOS app

See [ios/README.md](ios/README.md). The project is generated with xcodegen from `ios/project.yml`.
