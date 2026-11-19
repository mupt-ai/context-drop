<h1 align="center">Context Drop</h1>

---

<p align="center">
  <strong>A lightweight CLI for running a swarm of coding agents.</strong><br>
  Your Claude Code, Codex, Pi, and OpenCode sessions can message each other, hand each other files, and be overseen by an orchestrator agent that reads what every one of them is doing.
</p>

<p align="center">
  <a href="https://github.com/mupt-ai/context-drop/releases/latest"><img src="https://img.shields.io/badge/Releases-Download-111111?style=for-the-badge" alt="Download a release"></a>
  <a href="docs/cli.md"><img src="https://img.shields.io/badge/Docs-CLI_reference-0f9d76?style=for-the-badge" alt="Read the CLI reference"></a>
  <a href="docs/mods.md"><img src="https://img.shields.io/badge/Docs-Mods-5b4fd6?style=for-the-badge" alt="Read the mods guide"></a>
</p>

<p align="center">
  <a href="https://github.com/mupt-ai/context-drop/actions/workflows/test.yml"><img src="https://github.com/mupt-ai/context-drop/actions/workflows/test.yml/badge.svg" alt="Test"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/mupt-ai/context-drop" alt="License"></a>
</p>

Context Drop does three things for the agents on your machine:

- **Talk.** Any agent (or you) can message any other session, or a whole group, as its next prompt, and wait for the reply.
- **Share files.** Hand another agent a local file with `send --file`, or upload one for an expiring link to give a person.
- **Oversee.** An orchestrator agent can list every session, read its transcript, watch status changes, summarize the lot, and start or stop agents.

Join several machines to an **org** and the same commands reach agents on all of them. An org can also add its own commands, called **mods**: one TypeScript file each, run on the org's server, called from the CLI or by agents as tools.

It finds sessions from each agent's own on-disk state and running processes, so it doesn't matter where an agent runs: tmux, herdr, Rex, a plain terminal tab, or the Claude Code and Codex desktop apps. Claude Code subagents are listed under their parent session.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/cli/install.sh | bash
```

## Quick start

```sh
context-drop setup
```

Running `context-drop` with nothing configured offers the same setup. It's a terminal UI with one page per question, and each answer leaves a checkmark line behind:

- **Summary model:** a picker of cheap cloud models (Claude Haiku 5.5, GPT-6 Luna, Claude Sonnet 5.5) and the Ollama models installed on this machine. Cloud models use the key variable set in your shell, and setup tries each one until a test request works. You can also enter an Ollama model name, an OpenAI-compatible server, or a command. Only the variable's name is saved, never the key.
- **Uploads:** the public main server (`https://contextdrop.dev`), with no token needed, or a server you run.
- **The launcher:** tmux, herdr, or Rex, and whether agents start directly or through a wrapper such as `dari {agent}`.
- **The Pi extension**, if Pi is installed.

Cancelling at any point saves nothing. Answers go to `~/.context-drop/config.toml`.

Then:

```sh
context-drop list
```

```text
ID             AGENT     STATUS   ACTIVE  CWD          NAME                          WHERE
cc-6a79295a    claude    working  now     ~/code/app   app-ee                        rex calm-prairie/app
└ cc-43eeb069  claude    idle     2m      ~/code/app   sleeper — general-purpose     rex calm-prairie/app
cx-739b53b2    codex     idle     5m      ~/code/api                                 tmux work:2 %5
pi-0b9d88cf    pi        idle     now     ~/code/site  fix the nav                   herdr w1:p2
oc-079ea5e4    opencode  idle     12m     ~/code/cli   Quick acknowledgment request  -
```

`WHERE` is the tmux, herdr, or Rex pane the agent runs in, or `claude desktop` / `codex desktop` for a session in a desktop app. `--in`, `--session`, `--window`, and `--pane` filter by it, e.g. `context-drop list --in rex --session calm-prairie` or `context-drop list --in desktop`.

## Usage

### Read and message a session

```sh
context-drop read cc-6a79295a                          # recent transcript
context-drop read cc-6a79295a --screen                 # its terminal pane, dialogs included
context-drop send cc-6a79295a 'Also update the docs' --wait
```

`send` delivers the message as the session's next prompt. A busy agent gets it as a queued follow-up. `--wait` prints the reply. `--file PATH` (repeatable) lists a local file's absolute path under the message so the other agent reads it in place; nothing is uploaded.

An ID is a short hash of the agent and its native session ID, so a session always keeps the same ID. Commands also accept the native ID, a unique prefix of either, or the session name. Only live sessions appear: an agent process must be running.

| Agent | How a live session is found | How `send` delivers |
| --- | --- | --- |
| Claude Code | Its `~/.claude/sessions/<pid>.json` registry entry, while the process runs | Claude Code's peer-messaging socket. Subagents get messages through their parent (`SendMessage`). |
| Codex | A Codex process holds the thread's writer lock | `codex queue`, against the session's own app-server when it has one. For a Codex desktop thread, the app is then woken to pick it up. |
| Pi | The Context Drop Pi extension's registry, or a running `pi` process | The extension's socket. Without the extension: the tmux, herdr, or Rex pane it runs in. |
| OpenCode | The background service reports it running, or an OpenCode client in its directory has viewed it | The service's prompt API |

Pi has no built-in inbox. To message Pi sessions anywhere, install the extension with `context-drop setup pi` (or during setup), then restart running Pi sessions.

### Start an agent

```sh
context-drop start claude --cwd ~/code/app --prompt 'Fix the failing tests' --wait
context-drop start codex --in herdr
context-drop start pi --in rex -- --model fast
context-drop start claude --in rex --session calm-prairie --pane reviewer
context-drop start codex --in tmux --session work --window 2
```

`start` opens the agent in a tmux window (in a `context-drop` session), a herdr tab, or a Rex block. `--session`, `--window`, and `--pane` put it somewhere specific instead: a new window in that session (a tab in that herdr workspace), or a new pane split beside that window or pane (a Rex block). It waits until the agent appears in `context-drop list` and prints its ID. `--prompt` sends a first message, and `--wait` prints the reply. Arguments after `--` go to the agent.

`--in desktop` opens a new session in the Claude Code or Codex desktop app instead, in `--cwd` with `--prompt` filled into the composer:

```sh
context-drop start claude --in desktop --cwd ~/code/app --prompt 'Fix the failing tests'
```

The desktop apps never send a prompt on their own, so press Enter in the app. `start` waits (5 minutes by default) for the session to appear, then prints its ID. From then on it can be read and messaged like any other; `--wait` is not available, since you may edit the prompt before sending. `stop` can end a Claude Code desktop session, but not a Codex desktop thread, which runs inside the app's shared server: close it in the app.

If the new agent stops at a folder-trust prompt or a login screen, `start` says so and keeps waiting for you to answer it, and it never types `--prompt` into the dialog. `--trust` answers a folder-trust prompt by trusting the folder.

### Coordinate agents

```sh
context-drop watch                                     # a line whenever an agent starts, ends, or changes status
context-drop send --all --in rex --session calm-prairie 'Rebase on main before you push'
context-drop stop cc-6a79295a                          # --all with filters to stop a group
```

`send --all` messages every matching session except the sender. `stop` closes panes `start` opened and sends SIGTERM to everything else, and it refuses busy agents unless `--force`. Agents sharing a repository resolve conflicts the way people do, through git; an org that wants locks can add them as a mod (see the reference `lock` mod).

### Work across machines

```sh
context-drop join cdj_…            # once per machine, with the org's join token
context-drop daemon --share ~/code # keep it connected; share agents working under ~/code
context-drop list                  # agents on every machine, with IDs like laptop/cc-6a79295a
context-drop send devbox/cx-739b53b2 'Your migration broke the API tests' --wait
```

An org is a pool of agents across machines. Each machine joins once with the org's join token, then runs `daemon`, which holds one outbound connection to the server (no open port) and answers `read` and `send` for the agents it shares. The daemon makes you choose what to share: `--share DIR` (repeatable) or `--share-all`, and `--read-only` refuses every message from the org. An agent that isn't shared looks like one that doesn't exist.

The server stamps every message with the machine it came from, so no machine can speak as another, and a message from an agent arrives opening with `[context-drop] Agent laptop/cc-6a79295a, not your user.` so the recipient treats it as information rather than as its user's instructions. Orgs are created on the server with its admin token: on `https://contextdrop.dev`, ask for a join token; on your own server, see the [server guide](docs/server.md#orgs). The [CLI reference](docs/cli.md#orgs) covers `join`, `daemon`, `leave`, and how each command behaves across the org.

### Add org commands (mods)

```ts
// stuck.ts
import { mod, z } from "@contextdrop/mod"

export default mod({
  name: "stuck",
  description: "Agents that are blocked",
  input: z.object({ machine: z.string().optional() }),
  can: ["list"],
  async run({ machine }, cd) {
    return (await cd.list({ machine })).filter((a) => a.status === "blocked")
  },
})
```

```sh
context-drop deploy stuck.ts       # live for the whole org at once
context-drop stuck --machine devbox
claude mcp add context-drop -- context-drop mcp   # agents get list, read, send, and every mod as tools
```

A mod is one TypeScript file that the server runs in its own Deno process for each call, with only the capabilities its `can` list names: the org's `list`, `read`, and `send`, calling other mods, a key-value store, secrets, and outbound HTTP to named public hosts. Events can call it too (an agent starting, ending, or changing status, a cron schedule, or a signed webhook). Every deploy is a version, `--preview` deploys a copy only your machine can call, and `rollback`, `logs`, and `secret set` manage it. The [mods guide](docs/mods.md) has the full API; [`mods/`](mods) has two reference mods, `stuck` and `lock` (an org-wide lock on a path).

### Summarize everything

```sh
context-drop summarize
```

`summarize` reads each live session's transcript and asks your chosen model what it is doing, what changed, and whether it needs you. When more than one session is live, it adds an overview across them. Sessions with no new activity reuse their last summary, so repeated runs only pay for what changed. State lives in `~/.context-drop/summaries.json`. Transcript excerpts go to the configured model, so use a local provider if they must stay on your machine.

### Upload a file

```sh
context-drop upload ./report.txt --ttl 1h
```

Uploads go to the public server (`https://contextdrop.dev`) by default, or set `CONTEXT_DROP_ENDPOINT` for your own; `CONTEXT_DROP_UPLOAD_TOKEN` is only needed for private servers. To run the bundled server, see the [server guide](docs/server.md). Anyone with a download link can open the file until it expires. Use `upload` to share with a person or another machine; between agents on this machine, `send --file` is enough.

## Configuration

`context-drop setup` writes these keys to `~/.context-drop/config.toml`. You can also edit the file directly:

```toml
summarize_provider = "ollama"                   # anthropic, openai, ollama, or command
summarize_model = "qwen3.8:27b-mlx"
summarize_base_url = "http://localhost:11434"
# summarize_api_key_env = "ANTHROPIC_API_KEY"   # name of the variable holding the key
# summarize_command = "claude -p"               # for provider "command"

launch_in = "rex"                               # tmux, herdr, rex, or desktop
launch_command = "dari {agent}"                 # {agent} becomes claude, codex, pi, or opencode
```

`summarize` never picks a provider or key from your environment. Without a provider, it stops and offers setup. Flags such as `--provider` and `--in`, and `CONTEXT_DROP_*` environment variables, override the file. The [CLI reference](docs/cli.md) covers every command, flag, and variable.

## Limitations

- Discovery uses `ps` and `lsof` and is tested on macOS. On Linux, only the unit tests run in CI.
- Without the extension, a Pi session is identified by its process until it writes a session file, so its ID changes after its first message. `send --wait` and `start --wait` follow the change.
- Desktop-app sessions have no pane: `start --in desktop` cannot place them with `--session`, `--window`, or `--pane`, `read --screen` does not apply, and the app waits for you to press Enter on the first prompt. A desktop session appears only once its first message is sent.
- Codex desktop threads are found and sent to through the same `~/.codex` state as terminal Codex; delivery to them is new and less tested than other channels.
- `daemon` runs in the foreground; keep it running under your own service manager for now. Across an org, `--file`, `read --screen`, and the `--in`/`--session`/`--window`/`--pane` filters work only for this machine's agents.
- Mods need Deno on the server (`CONTEXT_DROP_DENO`); without it, mods are off. `ask`, `summarize`, and shared files are not available to mods yet.
- A newly started agent may stop at first-run prompts, such as trusting a folder or logging in, before it reads messages. `start` detects and reports them; only folder-trust prompts can be answered for you (`--trust`).

## Development

```sh
cd cli
make test     # go test ./...
make race     # with the race detector
```

## License

[MIT](LICENSE).
