# CLI reference

## Sessions

Context Drop indexes live Claude Code, Codex, Pi, and OpenCode sessions, plus Claude Code subagents. A session is live while the agent process that owns it runs. Each session gets a deterministic ID: a two-letter agent prefix (`cc`, `cx`, `pi`, `oc`) and the first eight hex digits of `sha256("<agent>:<native id>")`. Claude Code subagents use `<parent native id>/<subagent id>` as their native ID.

A `TARGET` is an ID, a native session ID, a unique prefix of either, or a session name. Ambiguous targets are rejected with the candidates listed.

### `context-drop list [--json] [--agent KIND] [--top-level]`

List live sessions with ID, agent, status (`idle`, `working`, `blocked`, `unknown`), last activity, working directory, and name. Subagents are listed under their parent. `--json` includes the native ID, parent, PID, TTY, transcript path, and the delivery channel `send` will use.

### `context-drop show TARGET`

Print one session as JSON.

### `context-drop read TARGET [-n N] [--json]`

Print the last `N` transcript messages (default 20, `0` for all). Tool calls appear as `[tool: name]`.

### `context-drop send TARGET MESSAGE... [--wait] [--timeout D] [--from NAME]`

Deliver a message into a live session as its next prompt; a busy agent receives it as a queued follow-up. Use `-` as the message to read it from stdin. `--wait` waits until the agent has replied and settled, then prints the new messages (default timeout 30 minutes).

Delivery per agent:

- **Claude Code**: the session's peer-messaging socket, authenticated with the token Claude Code publishes in `~/.claude/sessions`. The agent sees the message as coming from another session (`--from` sets the name). Subagents are reached through their parent, which is asked to forward the message with `SendMessage`; finished subagents stay addressable because `SendMessage` resumes them.
- **Codex**: `codex queue --thread ID`. Threads served by a private `codex app-server --listen ws://…` (for example a wrapper-launched Codex) are queued against that server using its token file.
- **Pi**: the socket opened by the Context Drop Pi extension (`context-drop setup pi`). Without the extension, the text is typed into the tmux, herdr, or Rex pane the Pi process runs in. A Pi session without the extension is identified by its process until it writes a session file, then by that file, so its ID changes after its first message; `send --wait` and `start --wait` follow it.
- **OpenCode**: `opencode api POST /api/session/ID/prompt` on the background service. Child sessions (subagents) accept messages directly.

### `context-drop summarize [flags]`

Summarize every live top-level session (subagents are folded into their parent) and, when there is more than one, write a short cross-session overview. Summaries persist in `~/.context-drop/summaries.json`; a session whose transcript has not changed since the last run reuses its summary without a model call, and a changed session is re-summarized from its previous summary plus the new messages.

Flags: `--provider anthropic|openai|ollama|command`, `--model`, `--base-url`, `--api-key-env`, `--command`, `--agent KIND`, `--force` (re-summarize everything), `--no-overview`, `--json`.

Settings come from `~/.context-drop/config.toml`, the environment, or flags, in that order of precedence (flags win). Nothing is inferred: `summarize` refuses to run until a provider is named, and it reads only the credential variable you name.

```toml
summarize_provider = "ollama"
summarize_model = "gpt-oss:20b"
# summarize_base_url = "http://localhost:11434"
# summarize_api_key_env = "MY_KEY"
# summarize_command = "claude -p"
```

Environment equivalents are `CONTEXT_DROP_SUMMARIZE_PROVIDER`, `_MODEL`, `_BASE_URL`, `_API_KEY_ENV`, and `_COMMAND`. Requirements per provider:

- `anthropic`: `summarize_model` and `summarize_api_key_env` (the name of the variable holding the key). The key is sent as both `x-api-key` and a bearer token, which Claude Code-style gateways accept. `base_url` defaults to `https://api.anthropic.com`.
- `openai`: `summarize_model`. `base_url` defaults to `https://api.openai.com/v1`; the key variable is required for that endpoint and optional for other OpenAI-compatible servers.
- `ollama`: `summarize_model`. `base_url` defaults to `http://localhost:11434`.
- `command`: `summarize_command`. It runs with `sh -c` and receives the prompt on stdin.

Transcript excerpts are sent to the chosen model. Use a local provider when transcripts must stay on the machine.

### `context-drop start AGENT [--cwd DIR] [--in tmux|herdr|rex] [--name NAME] [--command CMD] [--prompt TEXT] [--wait] [-- AGENT_ARGS...]`

Start `claude`, `codex`, `pi`, or `opencode` in a new terminal and print its ID once it appears in `context-drop list`.

- `--in`: `tmux` opens a window in a tmux session named `context-drop` (created if needed), `herdr` opens a tab in the current workspace and runs the agent in its shell, and `rex` runs the agent in a new block. The default is `launch_in` from the config, then the multiplexer you are running inside (`$TMUX`, `HERDR_ENV`, `REX_BLOCK`), then whichever of tmux, herdr, or rex is installed.
- `--cwd` defaults to the current directory, and `--name` labels the window, tab, or block (default `AGENT-DIRNAME`).
- `--command` (or `launch_command`) is the command line that starts the agent. `{agent}` becomes the agent name, so `dari {agent}` runs `dari claude`, `dari codex`, and so on. Without it, the agent's executable runs directly. Arguments after `--` are appended.
- `--prompt` sends a first message once the agent is up, and `--wait` prints the reply. OpenCode gets the prompt on its command line, because it only creates a session when it receives one.
- `--timeout` (default 60s) bounds the wait for the agent to appear. If it does not appear in time, `start` prints the pane and exits. If a prompt was given, it fails because the prompt was not sent.

A new agent may show its first-run prompts (such as trusting a folder) before it accepts messages.

### `context-drop setup`

Interactive setup in the terminal. It covers:

1. The summary model: provider, model, endpoint, and the name of the environment variable holding its key, with an optional test request.
2. The launcher: the multiplexer and the launch command.
3. The Pi extension, if Pi is installed.

It writes `~/.context-drop/config.toml` at the end. Cancelling saves nothing. Bare `context-drop` offers it when nothing is configured, and `summarize` offers it when no provider is set. Both only offer it at a terminal.

### `context-drop setup pi [--print]`

Install the Pi extension at `~/.pi/agent/extensions/context-drop.ts` (or under `PI_CODING_AGENT_DIR`). Pi sessions started afterwards publish `~/.context-drop/pi/<pid>.json` and listen on `~/.context-drop/pi/<pid>.sock` (mode 0600). `--print` writes the extension to stdout instead.

### Environment

- `CONTEXT_DROP_HOME` relocates `~/.context-drop`.
- `CONTEXT_DROP_<AGENT>_BIN` overrides an executable, e.g. `CONTEXT_DROP_CODEX_BIN`, `CONTEXT_DROP_OPENCODE_BIN`, `CONTEXT_DROP_TMUX_BIN`, `CONTEXT_DROP_HERDR_BIN`, `CONTEXT_DROP_REX_BIN`. Rex is otherwise found through `REX_BIN_DIR`, `PATH`, or the Rex app bundle.
- `CONTEXT_DROP_LAUNCH_IN` and `CONTEXT_DROP_LAUNCH_COMMAND` set the `start` defaults.
- `CODEX_HOME` and `PI_CODING_AGENT_DIR` are honoured.

Discovery uses `ps` and `lsof`.

## `context-drop upload [path]`

Upload one file to the configured TTL store. With no path, `--clipboard` uploads the current clipboard image.

Flags: `--endpoint`, `--ttl`, `--filename`, `--content-type`, `--clipboard`, `--no-clipboard`, and `--json`.

Public servers need no upload token. Private servers can require `CONTEXT_DROP_UPLOAD_TOKEN` or `upload_token` in `~/.context-drop/config.toml`.

## `context-drop version`

Print the binary version, source commit, and build date.
