# Configuration

Context Drop keeps local orchestration state under the platform-specific Context Drop home directory. Set `CONTEXT_DROP_HOME` to select an isolated root. Files containing tokens are written with user-only permissions.

## Upload client

The upload client reads `config.toml` from the Context Drop config directory, or `CONTEXT_DROP_CONFIG` when set.

| Key / environment variable | Purpose | Default |
|---|---|---|
| `endpoint` / `CONTEXT_DROP_ENDPOINT` | TTL upload service origin | `https://contextdrop.dev` |
| `upload_token` / `CONTEXT_DROP_UPLOAD_TOKEN` | Upload-only bearer credential | empty |
| `default_ttl` / `CONTEXT_DROP_TTL` | Default link lifetime | `24h` |
| `clipboard` | Enable clipboard integration by default | `false` |

Durations use Go syntax such as `15m`, `1h`, or `168h`. Command flags override the loaded endpoint or TTL for one upload.

## Runtime

The daemon creates `runtime/config.json` and a separate token file on first start. Useful bootstrap environment values include:

| Variable | Purpose |
|---|---|
| `CONTEXT_DROP_RUNTIME_PORT` | Loopback port, default `47762` |
| `CONTEXT_DROP_WORKER_AGENT` | Pool agent: `pi`, `codex`, or `claude` (persisted; also settable with `context-drop config worker-agent`) |
| `CONTEXT_DROP_HERDR_SESSION` | Herdr session, default `default` |
| `CONTEXT_DROP_FULL_AI_HERDR_WORKSPACE_LABEL` | Managed workspace label |
| `CONTEXT_DROP_RUNTIME_ADDRESS` | Advanced loopback client override |
| `CONTEXT_DROP_RUNTIME_ENTRY` | Advanced built-runtime entry override |

The runtime config records interactive launch argv for detected agents under `agents`, and `workerAgent` names the one the pool runs. The daemon needs Node.js 20+ and Herdr.

## iMessage adapter

There is no public setup command. An administrator provisions `<CONTEXT_DROP_HOME>/imessage/config.json` with mode `0600`. The schema is defined by `internal/imessage.Config`; important fields are:

- `enabled`, `trusted`, and `router_mode`
- exact `chat_id` and optional send `recipient`
- absolute `imsg_path`
- absolute `responder_command` argv containing `{prompt_file}`
- positive polling, history, responder, send, message-size, and reply-size limits; trusted persistent responder turns have a five-minute hard ceiling
- optional absolute persona, memory, archive, and responder-working-directory paths

Router mode requires a trusted private chat and a persistent Pi responder. The daemon warms the main session and four native workers of the configured agent. The main's only tool is `delegate_to_worker`; final responses use ordinary daemon-owned iMessage delivery. Old `delegate_all` and `yolo_mode` settings no longer select execution paths.

The pool runs `agents[workerAgent].command` in each worker tab. Worker panes, their agent kind, and capabilities persist in `runtime/worker-pool.json`; task forks, briefings, and worker outboxes live alongside it. Do not delete pool state while its native workers are alive.

Telegram is not implemented in this release.

## Server

The upload server is environment-only. See [Server and self-hosting](server.md) for the complete variables and examples.
