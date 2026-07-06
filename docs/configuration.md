# Configuration

`relaymux init` writes a core config with no message adapters at `~/.relaymux/config.json`. The file is private by default and relaymux refuses to overwrite it unless you pass `--force`.

relaymux stores private config, the first-party SQLite database, runtime files, logs, and local API token state under `~/.relaymux` by default:

```text
~/.relaymux/
  AGENTS.md       # primary local orchestrator instructions when present
  config.json     # private config, written mode 0600
  relaymux.sqlite3 # first-party relaymux SQLite database
  state/          # runtime files: prompts, scripts, schedules, daemon state, local API token
  logs/           # background service stdout/stderr logs
```

## SQLite Store

relaymux owns one canonical SQLite database at `<relaymux home>/relaymux.sqlite3`; with the default home this is `~/.relaymux/relaymux.sqlite3`. The path comes from `RELAYMUX_HOME` when set, otherwise the normal relaymux home, not the current working directory.

The first-party schema is managed by relaymux migrations and uses the `relaymux_` table prefix. Current managed tables are:

| Table | Purpose |
| --- | --- |
| `relaymux_schema_migrations` | Applied relaymux DB migrations. |
| `relaymux_metadata` | Small key/value metadata for the relaymux DB. |
| `relaymux_runs` | Generic run records for first-party local state. |
| `relaymux_events` | Generic run/event records for first-party local state. |

Use `relaymux db path`, `relaymux db init`, `relaymux db status`, and `relaymux db schema` to inspect or initialize the database. These commands use the system `sqlite3` CLI; relaymux does not install a native SQLite npm dependency.

New durable first-party state should be added here with migrations, not as new top-level home directories. Temporary file artifacts belong under `state/`.

Local extension or domain-specific tables can live in the same database, but relaymux only owns and migrates the `relaymux_` tables. Use a distinct table prefix for extension data.

## Core Config

Command arrays are argv templates. `{prompt}` and `{promptFile}` are substituted at launch time. The Pi commands below are examples, not requirements; edit them to match the agent CLIs you actually use.

```json
{
  "version": 1,
  "session": "agents",
  "stateDir": "~/.relaymux/state",
  "tmux": {
    "sessionMode": "shared"
  },
  "daemon": {
    "enabled": true,
    "host": "127.0.0.1",
    "port": 47761,
    "tokenFile": "~/.relaymux/state/webhook-token",
    "launchAgentLabel": "com.relaymux.daemon",
    "watchdog": {
      "enabled": true,
      "intervalSeconds": 60
    },
    "logDir": "~/.relaymux/logs"
  },
  "integrations": {},
  "orchestrator": {
    "cwd": "~",
    "command": ["pi", "--print", "--continue", "--session-dir", "~/.relaymux/state/sessions", "{prompt}"],
    "promptMode": "arg",
    "defaultSystemPrompt": true,
    "systemPromptFile": "",
    "extraSystemPrompt": ""
  },
  "agents": {
    "pi": { "command": ["pi", "{prompt}"], "promptMode": "arg" },
    "codex": { "command": ["codex", "{prompt}"], "promptMode": "arg" },
    "custom": { "command": ["sh", "-lc", "printf '%s\\n' \"$RELAYMUX_PROMPT\""], "promptMode": "env" }
  }
}
```

`daemon.launchAgentLabel` names the macOS LaunchAgent. On Linux the same value is reused as the systemd user service name with a `.service` suffix unless you set `daemon.systemdServiceName`.

## Agents And Orchestrator

An agent is a command template in `~/.relaymux/config.json`. If you configure `pi`, relaymux runs `pi ...`; if you configure `codex`, it runs `codex ...`; if you configure `custom`, it can be any shell command.

The orchestrator is also a command, but it has a different job from an agent. The orchestrator handles inbound requests from `relaymux ask` or optional message adapters. Agents do delegated work launched with `relaymux launch`. The orchestrator receives incoming text plus relaymux instructions and prints a reply on stdout.

relaymux includes a small built-in orchestration baseline by default: stay local-first, be concise, delegate work that may take more than about 10 seconds with `relaymux launch`, use the configured shared tmux session, prefer prompt files for longer delegated tasks, ask subagents to call `relaymux notify` with idempotency keys, keep quiet updates on `--reply-mode none`, use `relaymux schedule` for recurring prompts, keep durable relaymux records in SQLite and unavoidable file artifacts under `state/`, inspect real tmux/repo/test state before claiming completion, and never put secrets in prompts, logs, PRs, or replies.

Inline handling is meant only for truly tiny replies, lightweight read-only inspection, or explicit user requests to stay inline. Repo code changes, PR fixes, debugging/deploy work, deep research, CI loops, docs rewrites, long validation, and multi-file edits should normally become visible relaymux subagent runs.

Your relaymux home owns local orchestrator instructions. By default, relaymux appends `<relaymux home>/AGENTS.md` when that file exists; set `orchestrator.systemPromptFile` only when you want an explicit configured instructions file instead of the home `AGENTS.md`. relaymux never reads Pi's global `AGENTS.md` under `~/.pi/agent` for orchestrator instructions.

Your config owns local details: adapter tokens and chat IDs, exact agent CLI commands, local working directories, session names, private preferences, and repo-specific overrides. `orchestrator.extraSystemPrompt` remains an additive escape hatch for short local preferences. Set `orchestrator.defaultSystemPrompt` to `false` only if you want to remove relaymux's built-in orchestration baseline.

Example local override:

```json
{
  "orchestrator": {
    "defaultSystemPrompt": true,
    "systemPromptFile": "~/.relaymux/AGENTS.md",
    "extraSystemPrompt": "Use the custom agent first for documentation-only requests."
  }
}
```

## Prompt Passing

If a command contains `{prompt}` or `{promptFile}`, relaymux substitutes those values. Agent command templates can also use run context such as `{agent}`, `{name}`, `{repo}`, `{workdir}`, `{runId}`, and `{session}`. If the command does not contain a prompt placeholder, `promptMode` decides what to do with the prompt:

| `promptMode` | Behavior |
| --- | --- |
| `arg` | Append the prompt as a command-line argument unless `{prompt}` is already present. |
| `env` | Put the prompt in `RELAYMUX_PROMPT`. |
| `stdin` | Write the prompt under `~/.relaymux/state/prompts` and pipe that file to stdin. |
| `none` | Do not pass the prompt automatically. |

Use `--prompt @prompt.txt` or `--prompt-file prompt.txt` for longer prompts.

## Tmux Sessions

A run is one `relaymux launch`. By default, relaymux creates a new tmux window in one shared session named `agents`. tmux calls these windows; many terminal apps display them like tabs. relaymux does not create panes or splits for agent runs.

Launch a named agent in the default shared session:

```bash
relaymux launch --repo ~/code/my-app --agent pi --name fix-api --prompt @prompt.txt
```

Launch into a separate tmux session when you want an isolated group of windows:

```bash
relaymux launch --session release-fix --repo ~/code/my-app --agent codex --prompt @prompt.txt
```

Use per-worktree sessions when each Git worktree should get a stable session name. In this mode, relaymux derives the session from the repo/worktree/branch plus a short hash:

```bash
relaymux launch --session-mode per-worktree --repo ~/code/my-app --agent pi --prompt @prompt.txt
```

Inspect local state. `--history` includes old run records whose tmux windows have already exited:

```bash
relaymux status
relaymux status --history
relaymux status --session agents
```
