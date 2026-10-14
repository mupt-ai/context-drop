# CLI reference

## Herdr agents

These commands drive coding agents that run in [Herdr](https://herdr.dev) tabs. An assistant or script can call them to start, continue, and check on work. A target is a Herdr agent name or pane ID; nothing ever falls back to the focused pane.

### `context-drop agents [--json]`

List every agent herdr sees: name (or pane ID), kind, status (`idle`, `working`, `blocked`, `done`, `unknown`), tab title, and working directory.

### `context-drop new NAME PROMPT --workspace LABEL --cwd DIR [--agent claude|codex|pi] [--command EXECUTABLE] [--command-arg ARG] [--wait]`

Open a tab in the Herdr workspace labeled `LABEL`, start the agent in `DIR` through Herdr, name it `NAME`, and submit `PROMPT`. Install and configure the selected agent CLI in Herdr first. If startup fails, the tab is left open for inspection.

`--command EXECUTABLE` launches a custom executable instead of using `herdr agent start`; repeat `--command-arg ARG` for each argument (write `--command-arg=--flag` for values that start with `-`). Context Drop quotes each word for a POSIX-compatible shell and runs the result in the new pane, waits up to a minute for Herdr to detect the selected `--agent` kind there, names it `NAME`, then submits `PROMPT`. If the agent does not become ready or is blocked on input, `new` fails without retrying or sending the prompt and prints the pane ID. Inspect it with `context-drop read PANE`; if usable, run `herdr agent rename PANE NAME` and then `context-drop send NAME PROMPT`, otherwise close it with `herdr pane close PANE` before retrying. If only rename fails, `new` warns and submits to the pane ID.

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
