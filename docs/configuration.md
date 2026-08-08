# Configuration

Context Drop has two kinds of configuration. The CLI reads a local config file plus `CONTEXT_DROP_*` environment variables. The server reads environment variables only.

Durations use Go duration syntax, such as `30s`, `15m`, `1h`, or `24h`. Use `168h` for seven days; `7d` is not a valid duration string.

## CLI config file

Print the config file path with:

```sh
context-drop config path
```

By default, the file is named `config.toml` under your operating system's user config directory, usually something like `~/.config/context-drop/config.toml` on Linux or `~/Library/Application Support/context-drop/config.toml` on macOS. Set `CONTEXT_DROP_CONFIG` to use a different path.

Print the current config with saved tokens redacted:

```sh
context-drop config get
```

The CLI stores the following values in the config file:

| Key | Purpose | Default |
| --- | --- | --- |
| `endpoint` | Service endpoint used by CLI commands. | `https://contextdrop.dev` |
| `chain_id` | Current machine chain ID. | empty |
| `machine_id` | Current machine ID inside the chain. | empty |
| `machine_name` | Human-friendly machine name. | empty |
| `chain_session_token` | Token for chain-scoped commands. | empty |
| `default_ttl` | Default upload TTL. | `24h` |
| `clipboard` | Whether clipboard integration is enabled by default. | `false` |

The CLI writes the config file with user-only permissions. Keep it private because it can contain a chain session token.

## Set config values

Use `context-drop config set <key> <value>` for user-facing settings:

```sh
context-drop config set endpoint https://contextdrop.dev
context-drop config set default_ttl 24h
context-drop config set clipboard true
context-drop config set clipboard false
context-drop config set machine_name laptop
```

`ttl` is accepted as an alias for `default_ttl`, and `copy` is accepted as an alias for `clipboard`.

Use `--endpoint` or `--ttl` on an individual command when you only want a one-command override:

```sh
context-drop --endpoint https://drop.example.com --ttl 1h ./file.png
```

Use `--clipboard` for one command when the default is disabled, or `--no-clipboard` for one command when the default is enabled.

## CLI environment variables

Environment variables override values loaded from the config file for the current process. They are useful in CI, scripts, containers, or one-off commands.

| Variable | Purpose |
| --- | --- |
| `CONTEXT_DROP_CONFIG` | Path to the CLI config file. |
| `CONTEXT_DROP_ENDPOINT` | Service endpoint. |
| `CONTEXT_DROP_CHAIN_ID` | Runtime chain ID. |
| `CONTEXT_DROP_MACHINE_ID` | Runtime machine ID. |
| `CONTEXT_DROP_MACHINE_NAME` | Runtime machine name. |
| `CONTEXT_DROP_CHAIN_SESSION_TOKEN` | Runtime chain session token for chain-scoped commands. |
| `CONTEXT_DROP_TTL` | Runtime default upload TTL. |
| `CONTEXT_DROP_HOME` | Isolated private root for CLI config, runtime, daemon, schedules, logs, and tokens. |
| `CONTEXT_DROP_RUNTIME_PORT` | Runtime initialization port (default `47762`; must be 1–65535). |
| `CONTEXT_DROP_BACKEND` | Default session backend for launches (tmux or herdr; default `herdr`). |
| `CONTEXT_DROP_HERDR_SESSION` | Named Herdr session used for launches (default `default`). |
| `CONTEXT_DROP_RUNTIME_ADDRESS` | Advanced client override for the loopback runtime URL. |
| `CONTEXT_DROP_RUNTIME_ENTRY` | Advanced path override for the built Node runtime entry. |
| `CONTEXT_DROP_INBOX_INTERVAL` | Daemon inbox polling interval (default `1m`, minimum `10s`). |

Runtime credentials from environment variables are not written back to the config file by unrelated commands. Runtime token/config, daemon state, schedules, and logs live under the same private Context Drop root with user-only permissions. The runtime config records a `defaultBackend` (`tmux` or `herdr`) and a named `herdrSession`; `herdrPath` is resolved at initialization so the daemon can run Herdr even with a minimal service-manager PATH. Initialization resolves and persists an absolute `nodePath` in the runtime config so the OS service can start Node even when its environment has a minimal `PATH`; re-run `context-drop init` if Node moves.

A schedule snapshots its prompt when added, requires an absolute existing repository path, and accepts prompts up to 64 KiB. Interval schedules use `--every` (minimum one minute). Calendar schedules use mutually exclusive `--cron` and an IANA `--timezone`, for example `--cron '0 8,13,19 * * *' --timezone America/Los_Angeles`. Calendar evaluation follows local wall-clock time through DST. After downtime, the daemon launches at most one missed occurrence and advances directly to the next future occurrence rather than replaying a backlog.

Agent registrations can be updated without editing JSON directly: `context-drop agent configure --name NAME --command-json '["/absolute/agent","@{prompt_file}"]'`. Repeated `--arg` flags are also supported and preserve exact argv boundaries. Exactly one `{prompt_file}` placeholder is required, and an existing agent is not overwritten unless `--replace` is passed.

## iMessage adapter config

`context-drop imessage setup` writes `imessage/config.json` under `CONTEXT_DROP_HOME` with mode `0600`. It stores the enabled flag, exact chat ID, optional recipient label, absolute `imsg` executable, millisecond receive debounce (250ms by default), sync limits, history/responder/send timeouts, maximum message/reply bytes, and responder argv. The daemon prefers one long-lived `imsg watch` stream, restarts it with capped backoff from a cursor committed in daemon state, and falls back to interval history polling when the installed `imsg` does not support watch. Legacy `poll_seconds` configs remain valid and control either the watch debounce or fallback interval. The responder argv is an array and must contain `{prompt_file}`; it is never interpreted by a shell. The default safe Pi preset is `pi --print --no-session --no-context-files --no-tools --no-extensions @{prompt_file}`. Changing the configured chat resets the cursor and causes a fresh initial sync, so old messages in the new chat are not answered.

Trusted Pi configurations that identify a persistent session are executed through one long-lived Pi RPC process. The session file remains append-only. Before provider calls, Context Drop non-destructively removes old duplicated wrapper text and large compaction summaries from the working model context, retains recent final exchanges and the complete current tool loop, and points the agent to the unchanged durable session, memory, and archive sources for retrieval.

The daemon records the latest local runtime failure (for example an occupied loopback port owned by a different process) in its state and surfaces it in `context-drop daemon status` as `Runtime error`. Port conflicts are reported clearly and retried with capped backoff rather than silently selecting another port.

## Credential modes

In normal human mode, run `context-drop init` once on the first machine, then use `context-drop token create` and `context-drop join <token>` for additional machines.

In token-only mode, provide `CONTEXT_DROP_CHAIN_SESSION_TOKEN` at runtime. This is useful for short-lived scripts or machines where you already have a valid chain token and do not want to write credentials to disk.

In uninitialized URL passthrough mode, an unauthenticated CLI can print or copy an existing `http` or `https` URL. This mode does not upload files and does not allow list or pull.

## Installer environment variables

The install script also reads a few environment variables:

| Variable | Purpose |
| --- | --- |
| `CONTEXT_DROP_VERSION` | Install a specific release tag or version instead of the latest release. |
| `CONTEXT_DROP_INSTALL_DIR` | Install directory for the `context-drop` binary. |
| `INSTALL_DIR` | Backward-compatible install directory variable used when `CONTEXT_DROP_INSTALL_DIR` is not set. |

Use them on the `bash` side of the pipe so the installer process can read them:

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/install.sh | \
  CONTEXT_DROP_VERSION=v0.0.9 CONTEXT_DROP_INSTALL_DIR="$HOME/.local/bin" bash
```

## Server environment variables

The `context-drop-server` binary is configured with environment variables. Run `context-drop-server --help` for a short built-in summary.

| Variable | Required? | Default | Description |
| --- | --- | --- | --- |
| `CONTEXT_DROP_ADDR` | No | `:8080` | Address the HTTP server listens on. |
| `CONTEXT_DROP_BASE_URL` | No | `http://localhost:8080` | Public base URL used when generating drop links. |
| `CONTEXT_DROP_STORAGE` | No | `local` | Storage backend: `local` or `gcs`. |
| `CONTEXT_DROP_DATA_DIR` | Local only | `.data` | Directory for local storage. |
| `CONTEXT_DROP_GCS_BUCKET` | GCS only | empty | Google Cloud Storage bucket name. |
| `CONTEXT_DROP_GCS_PREFIX` | No | empty | Optional object prefix in the GCS bucket. |
| `CONTEXT_DROP_DEFAULT_TTL` | No | `24h` | Default TTL when an upload does not specify one. |
| `CONTEXT_DROP_JOIN_TOKEN_TTL` | No | `10m` | Default pairing join-token TTL. Must not exceed 15 minutes. |
| `CONTEXT_DROP_MAX_TTL` | No | `168h` | Maximum accepted drop TTL. |
| `CONTEXT_DROP_MAX_BYTES` | No | `26214400` | Maximum upload size in bytes. |

For GCS, the server also needs Google Cloud credentials through the normal Google authentication mechanisms, such as `GOOGLE_APPLICATION_CREDENTIALS` for a local service account file or workload identity on Cloud Run.

See [Server and self-hosting](server.md) for complete server examples.
