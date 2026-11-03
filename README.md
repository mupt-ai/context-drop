<h1 align="center">Context Drop</h1>

---

<p align="center">
  <strong>See, message, and launch every coding agent on your machine.</strong><br>
  One CLI for your live Claude Code, Codex, Pi, and OpenCode sessions: list them with stable IDs, read and message them, summarize what they are all doing, and start new ones in tmux, herdr, or Rex.
</p>

<p align="center">
  <a href="https://github.com/mupt-ai/context-drop/releases/latest"><img src="https://img.shields.io/badge/Releases-Download-111111?style=for-the-badge" alt="Download a release"></a>
  <a href="docs/cli.md"><img src="https://img.shields.io/badge/Docs-CLI_reference-0f9d76?style=for-the-badge" alt="Read the CLI reference"></a>
</p>

<p align="center">
  <a href="https://github.com/mupt-ai/context-drop/actions/workflows/test.yml"><img src="https://github.com/mupt-ai/context-drop/actions/workflows/test.yml/badge.svg" alt="Test"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/mupt-ai/context-drop" alt="License"></a>
</p>

Context Drop finds sessions from each agent's own on-disk state and running processes. It doesn't matter where an agent runs: tmux, herdr, Rex, or a plain terminal tab. Claude Code subagents are listed under their parent session. It also uploads files to a self-hosted store with expiring links.

## Install

On macOS or Linux, install the latest release to `~/.local/bin`, or to `/usr/local/bin` when it is writable:

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/cli/install.sh | bash
```

To build from source with Go 1.26.2+:

```sh
cd cli
make install    # installs context-drop to ~/.local/bin
```

Add `~/.local/bin` to your `PATH` if needed.

## Quick start

```sh
context-drop setup
```

Running `context-drop` with nothing configured offers the same setup. The setup wizard covers three things:

- **The summary model:** provider (local Ollama, Anthropic, any OpenAI-compatible server, or a command such as `claude -p`), model, endpoint, and the name of the environment variable that holds its key. Only the variable's name is saved, never the key. You can send a test request before saving.
- **The launcher:** tmux, herdr, or Rex, and whether agents start directly or through a wrapper such as `dari {agent}`.
- **The Pi extension**, if Pi is installed.

Cancelling at any point saves nothing. Answers go to `~/.context-drop/config.toml`.

Then:

```sh
context-drop list
```

```text
ID             AGENT     STATUS   ACTIVE  CWD                     NAME
cc-6a79295a    claude    working  now     ~/code/app              app-ee
└ cc-43eeb069  claude    idle     2m      ~/code/app              sleeper — general-purpose
cx-739b53b2    codex     idle     5m      ~/code/api
pi-0b9d88cf    pi        idle     now     ~/code/site             fix the nav
oc-079ea5e4    opencode  idle     12m     ~/code/cli              Quick acknowledgment request
```

## Usage

### Read and message a session

```sh
context-drop read cc-6a79295a                          # recent transcript
context-drop send cc-6a79295a 'Also update the docs' --wait
```

`send` delivers the message as the session's next prompt. A busy agent gets it as a queued follow-up. `--wait` prints the reply.

An ID is a short hash of the agent and its native session ID, so a session always keeps the same ID. Commands also accept the native ID, a unique prefix of either, or the session name. Only live sessions appear: an agent process must be running.

| Agent | How a live session is found | How `send` delivers |
| --- | --- | --- |
| Claude Code | Its `~/.claude/sessions/<pid>.json` registry entry, while the process runs | Claude Code's peer-messaging socket. Subagents get messages through their parent (`SendMessage`). |
| Codex | A Codex process holds the thread's writer lock | `codex queue`, against the session's own app-server when it has one |
| Pi | The Context Drop Pi extension's registry, or a running `pi` process | The extension's socket. Without the extension: the tmux, herdr, or Rex pane it runs in. |
| OpenCode | The background service reports it running, or an OpenCode client in its directory has viewed it | The service's prompt API |

Pi has no built-in inbox. To message Pi sessions anywhere, install the extension with `context-drop setup pi` (or during setup), then restart running Pi sessions.

### Start an agent

```sh
context-drop start claude --cwd ~/code/app --prompt 'Fix the failing tests' --wait
context-drop start codex --in herdr
context-drop start pi --in rex -- --model fast
```

`start` opens the agent in a tmux window (in a `context-drop` session), a herdr tab, or a Rex block. It waits until the agent appears in `context-drop list` and prints its ID. `--prompt` sends a first message, and `--wait` prints the reply. Arguments after `--` go to the agent.

### Summarize everything

```sh
context-drop summarize
```

`summarize` reads each live session's transcript and asks your chosen model what it is doing, what changed, and whether it needs you. When more than one session is live, it adds an overview across them. Sessions with no new activity reuse their last summary, so repeated runs only pay for what changed. State lives in `~/.context-drop/summaries.json`. Transcript excerpts go to the configured model, so use a local provider if they must stay on your machine.

### Upload a file

```sh
context-drop upload ./report.txt --ttl 1h
```

Uploads need a running server and its upload token. Set `CONTEXT_DROP_ENDPOINT` and `CONTEXT_DROP_UPLOAD_TOKEN`. To run the bundled server, see the [server guide](docs/server.md). Anyone with a download link can open the file until it expires.

## Configuration

`context-drop setup` writes these keys to `~/.context-drop/config.toml`. You can also edit the file directly:

```toml
summarize_provider = "ollama"                   # anthropic, openai, ollama, or command
summarize_model = "qwen3.8:27b-mlx"
summarize_base_url = "http://localhost:11434"
# summarize_api_key_env = "ANTHROPIC_API_KEY"   # name of the variable holding the key
# summarize_command = "claude -p"               # for provider "command"

launch_in = "rex"                               # tmux, herdr, or rex
launch_command = "dari {agent}"                 # {agent} becomes claude, codex, pi, or opencode
```

`summarize` never picks a provider or key from your environment. Without a provider, it stops and offers setup. Flags such as `--provider` and `--in`, and `CONTEXT_DROP_*` environment variables, override the file. The [CLI reference](docs/cli.md) covers every command, flag, and variable.

## Limitations

- Discovery uses `ps` and `lsof` and is tested on macOS. On Linux, only the unit tests run in CI.
- Without the extension, a Pi session is identified by its process until it writes a session file, so its ID changes after its first message. `send --wait` and `start --wait` follow the change.
- A newly started agent may stop at first-run prompts, such as trusting a folder, before it reads messages.

## Development

```sh
cd cli
make test     # go test ./...
make race     # with the race detector
```

## License

[MIT](LICENSE).
