# CLI reference

## Herdr agents

These commands drive coding agents that run in [Herdr](https://herdr.dev) tabs. An assistant or script can call them to start, continue, and check on work. A target is a Herdr agent name or pane ID; nothing ever falls back to the focused pane.

### `context-drop agents [--json]`

List every agent herdr sees: name (or pane ID), kind, status (`idle`, `working`, `blocked`, `done`, `unknown`), tab title, and working directory.

### `context-drop new NAME PROMPT --workspace LABEL --cwd DIR [--agent claude|dari-claude|codex|pi] [--wait]`

Open a tab in the Herdr workspace labeled `LABEL`, start the agent in `DIR` through Herdr, name it `NAME`, and submit `PROMPT`. Install and configure the selected agent CLI in Herdr first. If startup fails, the tab is left open for inspection.

`--agent dari-claude` launches Claude Code through Dari (`dari claude`) instead of plain `claude`. Herdr's `agent start` can only run an agent's own executable, so Context Drop runs `dari claude` in the new tab's shell, waits up to a minute for Herdr to detect an idle Claude agent in that pane, names it `NAME`, and then submits `PROMPT`. Herdr lists the agent with kind `claude`. If Claude does not become ready, stops at a prompt such as folder trust or login, or the command cannot be typed, `new` fails without retrying or sending the prompt and prints the pane ID. Inspect the pane with `context-drop read PANE`. If Claude is usable, run `herdr agent rename PANE NAME` and then `context-drop send NAME PROMPT`; otherwise close the tab with `herdr pane close PANE` before running `new` again. If only the rename fails, `new` warns and sends the prompt to the pane ID.

The workspace label must match exactly one workspace, and `NAME` must not already be taken. If using a Git worktree, create it first and pass its absolute path as `--cwd`.

### `context-drop send TARGET TEXT [--force] [--wait]`

Submit a prompt to an existing agent. Refuses unless the agent is `idle` or `done`; `--force` sends anyway.

### `context-drop wait TARGET` and `--wait`

Block until the agent is `idle`, `done`, or `blocked`, then print its status and recent output. `blocked` usually means it is asking a question. `--wait` on `new` and `send` tracks the turn that prompt started; standalone `wait` is for an agent that is already working and returns immediately if it is idle. Both default to a six-hour `--timeout`.

### `context-drop read TARGET [--lines N]`

Print the agent's recent terminal output.

## `context-drop upload [path]`

Upload one file to the configured TTL store. With no path, `--clipboard` uploads the current clipboard image.

Flags: `--endpoint`, `--ttl`, `--filename`, `--content-type`, `--clipboard`, `--no-clipboard`, and `--json`.

Uploads require `CONTEXT_DROP_UPLOAD_TOKEN` or `upload_token` in `~/.context-drop/config.toml`.

## `context-drop version`

Print the binary version, source commit, and build date.
