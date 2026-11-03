# Context Drop

Context Drop finds every live coding-agent session on your machine — Claude Code, Codex, Pi, and OpenCode, including Claude Code subagents — and gives each one a stable ID. With that ID you can read the session's transcript, send it a message, or have a model of your choice summarize what all of your agents are doing. It does not depend on any terminal multiplexer: sessions are found from each agent's own on-disk state and running processes, so it works whether an agent runs in tmux, herdr, Rex, a plain terminal tab, or anything else. It can also start new agents in a tmux window, herdr tab, or Rex block.

It also uploads files to a self-hosted store with expiring links.

## Build from source

Requires Go 1.26.2+. From this repository:

```sh
cd cli
make install                 # installs context-drop to ~/.local/bin
~/.local/bin/context-drop --help
```

Add `~/.local/bin` to your `PATH` if necessary. No release binaries are published yet.

## Use

```sh
context-drop list                       # live sessions and subagents with their IDs
context-drop read cc-6a79295a           # recent transcript
context-drop send cc-6a79295a 'Also update the docs' --wait
context-drop summarize                  # what is every agent doing? (only re-summarizes what changed)
```

```text
ID             AGENT     STATUS   ACTIVE  CWD                     NAME
cc-6a79295a    claude    working  now     ~/code/app              app-ee
└ cc-43eeb069  claude    idle     2m      ~/code/app              sleeper — general-purpose
cx-739b53b2    codex     idle     5m      ~/code/api
pi-0b9d88cf    pi        idle     now     ~/code/site             fix the nav
oc-079ea5e4    opencode  idle     12m     ~/code/cli              Quick acknowledgment request
```

An ID is a short hash of the agent kind and its native session ID, so the same session always has the same ID. Commands also accept the native session ID, a unique prefix of either, or the session name.

Only live sessions are listed: an agent process must be running for its session to appear.

| Agent | How a live session is found | How `send` delivers |
| --- | --- | --- |
| Claude Code | `~/.claude/sessions/<pid>.json` registry with a running process | Claude Code's peer-messaging socket; subagents through their parent (`SendMessage`) |
| Codex | A Codex process holds the thread's writer lock | `codex queue` (against the session's own app-server when it has one) |
| Pi | The Context Drop Pi extension's registry, or a running `pi` process | The extension's socket; without the extension, the tmux, herdr, or Rex pane it runs in |
| OpenCode | The background service reports it running, or an OpenCode client in its directory has viewed it | The service's prompt API |

Pi has no built-in inbox, so install the bundled extension once and restart running Pi sessions:

```sh
context-drop setup pi
```

## Starting agents

```sh
context-drop start claude --cwd ~/code/app --prompt 'Fix the failing tests' --wait
context-drop start codex --in herdr
context-drop start pi --in rex -- --model fast
```

`start` opens the agent in a tmux window (in a `context-drop` session), a herdr tab, or a Rex block. It waits until the agent shows up in `context-drop list` and prints its ID. With `--prompt` it sends a first message, and `--wait` prints the reply. Arguments after `--` go to the agent.

Set defaults in `~/.context-drop/config.toml`:

```toml
launch_in = "rex"                 # tmux, herdr, or rex; otherwise the one you are in, then whichever is installed
launch_command = "dari {agent}"   # how agents are started; {agent} becomes claude, codex, pi, or opencode
```

## Summaries

`context-drop summarize` reads each live session's transcript and asks a model what it is doing, what changed, and whether it needs you. Results are stored in `~/.context-drop/summaries.json`; on the next run, sessions with no new transcript activity reuse their last summary and changed sessions are summarized from the previous summary plus the new messages.

Pick the model with flags or `summarize_*` keys in `~/.context-drop/config.toml`:

```sh
context-drop summarize --provider ollama --model gpt-oss:20b           # local
context-drop summarize --provider anthropic --model claude-sonnet-5-5
context-drop summarize --provider openai --base-url http://localhost:1234/v1 --model qwen3   # any OpenAI-compatible server
context-drop summarize --provider command --command 'claude -p'         # any CLI that reads stdin
```

With nothing configured it uses Anthropic when `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set, OpenAI when `OPENAI_API_KEY` is set, and otherwise a local Ollama server.

## File uploads

```sh
context-drop upload ./report.txt --ttl 1h
```

Uploads require a running server and its upload token. Set `CONTEXT_DROP_ENDPOINT` to the server URL and `CONTEXT_DROP_UPLOAD_TOKEN` to its token. The [server guide](docs/server.md) shows how to run the bundled server with local storage. Download links are accessible to anyone who has the URL until they expire.

See the [CLI reference](docs/cli.md) for every command and option. Run tests with `cd cli && make test`.

## License

[MIT](LICENSE).
