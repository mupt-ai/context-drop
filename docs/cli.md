# CLI reference

## Sessions

Context Drop indexes live Claude Code, Codex, Pi, and OpenCode sessions, plus Claude Code subagents. A session is live while the agent process that owns it runs. Each session gets a deterministic ID: a two-letter agent prefix (`cc`, `cx`, `pi`, `oc`) and the first eight hex digits of `sha256("<agent>:<native id>")`. Claude Code subagents use `<parent native id>/<subagent id>` as their native ID.

A `TARGET` is an ID, a native session ID, a unique prefix of either, or a session name. Ambiguous targets are rejected with the candidates listed.

## Orgs

An org is a pool of agents across many machines. Each machine joins once; from then on `list`, `read`, and `send` work across the whole org, and agents have org IDs like `avyay-mbp/cc-49901f7f` (`<machine>/<session>`).

### `context-drop join TOKEN [--server URL] [--name NAME]`

Exchange an org's join token for a credential for this machine, saved with mode 0600 in `~/.context-drop/org.json`. The machine's name defaults to its hostname (`Avyays-MBP.local` becomes `avyays-mbp`) and must be unique in the org. The server defaults to `https://contextdrop.dev`.

### `context-drop daemon (--share DIR... | --share-all) [--read-only]`

Keep this machine connected to its org: report its shared agents every 2 seconds (whenever the list changes, and at least every 30 seconds) and answer `read` and `send` for them. It holds one outbound WebSocket, so the machine needs no open port, and reconnects with backoff when the connection drops. While it is not running, the machine's agents show as `offline` to the rest of the org. It runs in the foreground; run it under your own service manager for now.

A shared agent can be read by anyone in the org and, unless `--read-only`, sent instructions it may act on with its own permissions, so the daemon makes you choose what to share: `--share DIR` (repeatable; `~` is expanded) shares agents working in that directory or below it, and `--share-all` shares every agent on the machine. To the org, an agent that is not shared looks like one that does not exist. `--read-only` refuses every message from the org.

Once joined, `upload` defaults to the org's server when no endpoint is configured.

### Mods

An org adds its own commands as mods: one TypeScript file each, run on the org's server. The [mods guide](mods.md) covers writing one; these commands manage and call them.

#### `context-drop deploy FILE`

Upload a mod and make it live for the whole org at once. Deploying a file whose mod has the same name replaces it; its key-value data is kept.

#### `context-drop mods [--json]`

List the org's mods with their capabilities and author.

#### `context-drop call NAME [--FIELD VALUE]... [--json INPUT] [--print-json]`

Run a mod. Flags fill its input and are converted to the input's types (a repeated flag fills a list); a single positional argument fills the mod's only required field. `--json` passes the whole input as JSON. Text output prints as is, a list of objects as a table, and anything else as JSON; `--print-json` always prints JSON. `context-drop NAME ...` is the same as `context-drop call NAME ...` when `NAME` is not a built-in command, and `context-drop NAME --help` shows the mod's input.

#### `context-drop mcp`

Run an MCP server on standard input and output that gives an agent `list`, `read`, `send`, `ask`, `slack_read`, and every mod as tools, and tells it when the list changes, so a newly deployed mod reaches running agents without a restart. `send` also takes `#channel`, `@person`, or `slack:CHANNEL/TS` to message people on the org's Slack. Add it to Claude Code with `claude mcp add context-drop -- context-drop mcp`.

### Slack

`context-drop slack install | connect | status | allow @PERSON... | disallow @PERSON... | read TARGET | uninstall | manifest` connects the org to a Slack workspace and manages who may answer agents. `install` and `connect` open the browser: `install` connects the workspace once per org, and `connect` links your own Slack account by signing in with Slack. `send '#channel' MESSAGE`, `send @person MESSAGE`, and `send slack:CHANNEL/TS MESSAGE` post to people; `--wait` prints their first replies. See the [Slack guide](slack.md).

### `context-drop leave [--force]`

Remove this machine from the org and revoke its credential. `--force` forgets the org locally even when the server cannot be reached.

### Across the org

- `list` shows every agent in the org, most recently active first, each main agent followed by its subagents. `--machine M` and `--cwd PATH` (that directory or below) narrow it, as does `--agent KIND`; `--local` shows only this machine with local IDs, as before joining. `--in`, `--session`, `--window`, and `--pane` need `--local` for now. `--json` prints `Agent` objects: `id`, `machine`, `kind`, `role` (`main`, `subagent`), `parent`, `root`, `agent_type`, `status` (including `offline`), `cwd`, `name`, `location`, `location_label`, `started_at`, `last_active`.
- `read MACHINE/SESSION` reads that agent wherever it runs. It shows the current context run, from the last compaction on; `--all` shows every run, `-n` the last N entries, and `--after CURSOR` only entries newer than the `cursor` a previous `read --json` returned. `--json` prints `{agent, run, runs, entries, cursor}`. `--screen` works for this machine's agents only.
- `send MACHINE/SESSION MESSAGE` delivers through the owning machine's daemon; the server sets the sender to this machine's name, so no machine can speak as another; `--from NAME` only adds a label in front (`NAME@machine`). When an agent sends, the recipient is told its org ID (`laptop/cc-1a2b3c4d`) to reply to. `--wait` works for one agent on another machine: it reads that agent until it has answered and settled. `send --all` goes to every connected main agent in the org, narrowed by `--machine`, `--cwd`, and `--agent`; `--local` keeps it to this machine. `--file` works only for this machine's agents; use `upload` for a link. An org ID for one of this machine's own agents is delivered locally, without the server.

### `context-drop list [--json] [--agent KIND] [--top-level] [--in MUX] [--session S] [--window W] [--pane P]`

List live sessions with ID, agent, status (`idle`, `working`, `blocked`, `unknown`), last activity, working directory, name, and where it runs. Subagents are listed under their parent. `--json` includes the native ID, parent, PID, TTY, transcript path, the delivery channel `send` will use, `location`, and `app` (`claude-desktop` or `codex-desktop` for a session in a desktop app). A desktop session shows `claude desktop` or `codex desktop` under `WHERE`, and `--in desktop` keeps only those.

Sessions are found from each agent's own state and processes, so an agent is listed wherever it runs, including panes Context Drop did not start. `WHERE` (`location` in JSON) is the tmux, herdr, or Rex pane its process runs in, found from the pane's terminal (tmux) or process tree (herdr, Rex), across every Rex session. It is `-` for a plain terminal tab. `location` has `mux`, `session`, `window`, `pane`, and `label`:

| | `session` | `window` | `pane` | `label` |
| --- | --- | --- | --- | --- |
| tmux | session name | window index | pane ID (`%3`) | |
| herdr | workspace ID | tab ID | pane ID (`w1:p2`) | |
| Rex | session label | window ID | block ID | block label |

`--in`, `--session`, `--window`, and `--pane` keep only the sessions in that place. Each takes an ID or a label at that level (herdr workspace and tab labels, tmux window names for `--window`, Rex block labels for `--pane`). Rex IDs also match without their `block:`/`window:`/`session:` prefix, or by the short form Rex prints (at least six characters).

Finding Rex blocks runs `rex` with `--autostart=false`, so `list` never starts a Rex server.

### `context-drop read TARGET [-n N] [--json] [--screen]`

Print the last `N` transcript messages (default 20, `0` for all). Tool calls appear as `[tool: name]`.

`--screen` prints the last `N` lines (default 40) of the tmux, herdr, or Rex pane the session runs in instead. Use it to see prompts and dialogs that never reach the transcript.

### `context-drop send --ask TARGET QUESTION...`

Answer QUESTION from the agent's transcript, without interrupting it. The end of the transcript (its user's prompts and its replies, up to about 100k tokens) goes to `claude -p --model haiku --no-session-persistence --tools Read,Grep,Glob --strict-mcp-config` with hooks off, run in the agent's directory on the machine that runs it, so the answerer can also read files there but not change them. Nothing is saved and the agent never sees the question. The answerer is told who asked and to say so when the transcript doesn't tell. Use it for what the agent knows or has done; anything it must agree to or act on is a message. It works for every kind of agent, including Claude Code subagents. Across an org, the owning machine's daemon answers; machines shared `--read-only` answer too, since nothing changes.

### `context-drop send TARGET MESSAGE... [--file PATH]... [--wait] [--timeout D] [--from NAME]`
### `context-drop send --all [--agent KIND] [--in MUX] [--session S] [--window W] [--pane P] MESSAGE...`

Deliver a message into a live session as its next prompt; a busy agent receives it as a queued follow-up. When the sender is an agent (the session running `send`, found from its process ancestry), the message opens with `[context-drop] Agent <id>, not your user. Reply: context-drop send <id> "..."`, for every kind of agent; `--from NAME` names a sender that is not an agent (`[context-drop] From NAME.`). A message a person sends from a plain terminal arrives as is. Use `-` as the message to read it from stdin. `--file PATH` (repeatable) hands the recipient a file or directory on this machine: its absolute path and size are listed under the message for the agent to read in place, and nothing is uploaded; a missing path is an error. `--wait` waits until the agent has replied and settled, then prints the new messages (default timeout 30 minutes).

`--all` sends the message to every live top-level session that matches the filters (the same filters as `list`), except the session running the command. When the command runs under a process that serves several sessions (a Codex app-server, or OpenCode), all of those sessions are skipped, since it cannot tell which one is the sender. Each delivery is reported on stderr; a failed one does not stop the others, and `send` exits non-zero if any failed. With `--wait` it waits for all the replies at once and prints each under `== ID`. The filters are refused without `--all`, so a forgotten `--all` never turns a target into part of the message.

Delivery per agent:

- **Claude Code**: the session's peer-messaging socket, authenticated with the token Claude Code publishes in `~/.claude/sessions`. The agent sees the message as coming from another session, named by the sending agent's ID or `--from`. Subagents are reached through their parent, which is asked to forward the message with `SendMessage`; finished subagents stay addressable because `SendMessage` resumes them.
- **Codex**: `codex queue --thread ID`. Threads served by a private `codex app-server --listen ws://…` (for example a wrapper-launched Codex) are queued against that server using its token file. For a thread in the Codex desktop app, `send` then opens `codex://threads/wake/ID` in the background so the app picks the message up.
- **Pi**: the socket opened by the Context Drop Pi extension (`context-drop setup pi`). Without the extension, the text is typed into the tmux, herdr, or Rex pane the Pi process runs in. A Pi session without the extension is identified by its process until it writes a session file, then by that file, so its ID changes after its first message; `send --wait` and `start --wait` follow it.
- **OpenCode**: `opencode api POST /api/session/ID/prompt` on the background service. Child sessions (subagents) accept messages directly.

### `context-drop stop [TARGET...] [--all [filters]] [--force] [--timeout D]`

Stop the named sessions, or with `--all` every live top-level session matching the filters (never the session running the command). `stop` waits up to `--timeout` (default 10s) for each to leave `list`.

- A session in a pane that `context-drop start` opened for it has that pane closed (`tmux kill-pane`, `herdr pane close`, `rex block close`). `start` records these panes in `~/.context-drop/launched.json`, keyed by the session and its process, so a record never applies to a later run of the session or to a reused pane ID. This also stops OpenCode sessions and Codex threads served by a shared app-server, as long as `start` opened them.
- Any other session gets SIGTERM, so a pane or terminal you opened yourself keeps its shell.
- A process that serves several sessions, a Codex `app-server` or `opencode serve`, is never signalled; close that session's terminal instead.
- Sessions that are `working` or `blocked`, and your own session, are refused unless `--force`. Subagents cannot be stopped on their own.


### `context-drop start AGENT [--cwd DIR] [--in tmux|herdr|rex|desktop] [--session S] [--window W] [--pane P] [--name NAME] [--command CMD] [--prompt TEXT] [--wait] [--trust] [-- AGENT_ARGS...]`

Start `claude`, `codex`, `pi`, or `opencode` in a new terminal and print its ID once it appears in `context-drop list`.

- `--in`: `tmux` opens a window in a tmux session named `context-drop` (created if needed), `herdr` opens a tab in the current workspace and runs the agent in its shell, and `rex` runs the agent in a new block. The default is `launch_in` from the config, then the multiplexer you are running inside (`$TMUX`, `HERDR_ENV`, `REX_BLOCK`), then whichever of tmux, herdr, or rex is installed.
- `--session`, `--window`, and `--pane` name where to open the agent, in the multiplexer's own terms. Each takes an ID or a label, and the place must already exist:

  | | `--session` | `--window` | `--pane` |
  | --- | --- | --- | --- |
  | tmux | session | window (index, `@ID`, or name) | pane (`%ID` or `SESSION:WINDOW.PANE`) |
  | herdr | workspace | tab | pane |
  | Rex | session | window | block |

  `--session` alone opens a new window there (a tab in herdr). `--window` or `--pane` opens a new pane split to the right of that window's focused pane, or of that pane. `start` never types into an existing pane. For example, `context-drop start claude --in rex --session calm-prairie` runs `rex run -s calm-prairie …`, and adding `--pane reviewer` splits beside the block labelled `reviewer`. Herdr commands reach the herdr session you are in (or `HERDR_SESSION`).
- `--cwd` defaults to the current directory, and `--name` labels the window, tab, or block (default `AGENT-DIRNAME`). A tmux split has no label.
- `--command` (or `launch_command`) is the command line that starts the agent. `{agent}` becomes the agent name, so `dari {agent}` runs `dari claude`, `dari codex`, and so on. Without it, the agent's executable runs directly. Arguments after `--` are appended.
- `--prompt` sends a first message once the agent is up, and `--wait` prints the reply. OpenCode gets the prompt on its command line, because it only creates a session when it receives one.
- `--timeout` (default 60s) bounds the wait for the agent to appear. If it does not appear in time, `start` prints the pane and exits. If a prompt was given, it fails because the prompt was not sent.
- While it waits, `start` reads the new pane. If the agent stops at a folder-trust prompt (Claude Code, Codex) or a first-run screen such as a login, `start` says so on stderr at once and keeps waiting, so you can answer it in the pane. If it is still there at the timeout, `start` reports it instead of timing out silently, and with `--prompt` it fails without typing the prompt into the dialog.
- `--trust` answers a folder-trust prompt by choosing the option that trusts the folder (it presses Down until that option is selected, then Enter). It never answers login screens, and it never picks an option that also waives approvals.

`--in desktop` opens a new session in the agent's desktop app instead of a terminal: `claude://code/new` for Claude Code, `codex://threads/new` for Codex, in `--cwd`, with `--prompt` filled into the composer. The apps fill the prompt in but never send it, and they create the session only when its first message is sent, so press Enter in the app. `start` waits for a new desktop session in that directory (`--timeout`, 5 minutes by default here) and prints its ID. `--wait` is refused, since the prompt may be edited before it is sent; follow the session with `read`. A Codex desktop thread runs in the app's shared server, so `stop` cannot end it. `--session`, `--window`, `--pane`, `--command`, and agent arguments do not apply. Other agents have no desktop app.

`--json` prints the launch result: `mux`, `pane`, `location`, `command`, `registered`, `session`, and `status`, one of `registered`, `waiting_for_trust`, `waiting_for_setup`, `not_registered`, or `started` (with `--timeout 0`).

### `context-drop setup`

Interactive setup in the terminal. It covers:

1. The launcher: the multiplexer and the launch command.
2. Uploads: the main server or one you run.
3. The Pi extension, if Pi is installed.

It writes `~/.context-drop/config.toml` at the end. Cancelling saves nothing. Bare `context-drop` offers it at a terminal when nothing is configured.

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
