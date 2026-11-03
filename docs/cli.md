# CLI reference

## Sessions

Context Drop indexes live Claude Code, Codex, Pi, OpenCode, and Muse sessions and their subagents. A session is live while the agent process that owns it runs. Each session gets a deterministic ID: a two-letter agent prefix (`cc`, `cx`, `pi`, `oc`, `mu`) and the first eight hex digits of `sha256("<agent>:<native id>")`. Subagents use `<parent native id>/<subagent id>` as their native ID.

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
- **Pi**: the socket opened by the Context Drop Pi extension (`context-drop setup pi`). Without the extension, a Pi session in tmux is typed into instead.
- **OpenCode**: `opencode api POST /api/session/ID/prompt` on the background service. Child sessions (subagents) accept messages directly.
- **Muse**: typed into the session's tmux pane when there is one, otherwise `muse session-message send`. Muse subagents are reached through their parent.

### `context-drop summarize [flags]`

Summarize every live top-level session (subagents are folded into their parent) and, when there is more than one, write a short cross-session overview. Summaries persist in `~/.context-drop/summaries.json`; a session whose transcript has not changed since the last run reuses its summary without a model call, and a changed session is re-summarized from its previous summary plus the new messages.

Flags: `--provider anthropic|openai|ollama|command`, `--model`, `--base-url`, `--api-key-env`, `--command`, `--agent KIND`, `--force` (re-summarize everything), `--no-overview`, `--json`.

Defaults come from `~/.context-drop/config.toml` or the environment:

```toml
summarize_provider = "ollama"
summarize_model = "gpt-oss:20b"
# summarize_base_url = "http://localhost:11434"
# summarize_api_key_env = "MY_KEY"
# summarize_command = "claude -p"
```

Environment equivalents are `CONTEXT_DROP_SUMMARIZE_PROVIDER`, `_MODEL`, `_BASE_URL`, `_API_KEY_ENV`, and `_COMMAND`. With no provider set, Context Drop uses Anthropic when `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set (honouring `ANTHROPIC_BASE_URL`), OpenAI when `OPENAI_API_KEY` is set (`--model` required), and otherwise Ollama at `OLLAMA_HOST` or `http://localhost:11434`. The `command` provider runs the command with `sh -c` and writes the prompt to its stdin.

Transcript excerpts are sent to the chosen model. Use a local provider when transcripts must stay on the machine.

### `context-drop setup pi [--print]`

Install the Pi extension at `~/.pi/agent/extensions/context-drop.ts` (or under `PI_CODING_AGENT_DIR`). Pi sessions started afterwards publish `~/.context-drop/pi/<pid>.json` and listen on `~/.context-drop/pi/<pid>.sock` (mode 0600). `--print` writes the extension to stdout instead.

### Environment

- `CONTEXT_DROP_HOME` relocates `~/.context-drop`.
- `CONTEXT_DROP_<AGENT>_BIN` overrides an executable, e.g. `CONTEXT_DROP_CODEX_BIN`, `CONTEXT_DROP_OPENCODE_BIN`, `CONTEXT_DROP_MUSE_BIN`, `CONTEXT_DROP_TMUX_BIN`.
- `CODEX_HOME`, `XDG_DATA_HOME` (Muse), and `PI_CODING_AGENT_DIR` are honoured.

Discovery uses `ps` and `lsof`.

## `context-drop upload [path]`

Upload one file to the configured TTL store. With no path, `--clipboard` uploads the current clipboard image.

Flags: `--endpoint`, `--ttl`, `--filename`, `--content-type`, `--clipboard`, `--no-clipboard`, and `--json`.

Uploads require `CONTEXT_DROP_UPLOAD_TOKEN` or `upload_token` in `~/.context-drop/config.toml`.

## `context-drop version`

Print the binary version, source commit, and build date.
