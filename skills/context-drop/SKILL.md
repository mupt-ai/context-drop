---
name: context-drop
description: Use the Context Drop CLI to list, read, start, prompt, and wait for coding agents in Herdr, or upload temporary files with expiring links. Use when asked to coordinate a Herdr coding agent through Context Drop or share a temporary file.
---

# Context Drop

`context-drop` drives coding agents in Herdr tabs and uploads files to a configured expiring-link store. Agent commands require an exact name or pane ID from `context-drop agents`; they never fall back to the focused pane.

## Inspect an agent

```bash
context-drop agents
context-drop read TARGET
```

Statuses: `working` is mid-turn; `idle` and `done` are ready for input; `blocked` is waiting on an approval or question; `unknown` means Herdr cannot tell. Choose a target by its name, tab title, or working directory. If several match, ask which one.

## Continue or wait

```bash
context-drop send TARGET "what to do next" --wait
context-drop wait TARGET
```

`send` refuses a `working`, `blocked`, or `unknown` agent unless `--force` is specified. Ask before forcing a prompt into a busy agent. `wait` observes an agent already working and returns when it settles. Both waits default to a six-hour timeout. Read and report the agent's actual output; starting or prompting is not proof of finished work.

## Start an agent

Choose or create a project directory (for isolated coding work, a Git worktree is useful). Select an existing Herdr workspace label, a unique lowercase agent name, and an agent CLI installed and configured for Herdr:

```bash
context-drop new NAME "the task and relevant context" \
  --workspace LABEL --cwd /absolute/path/to/project --agent pi --wait
```

Supported agent kinds are `claude` (default), `codex`, and `pi`. Brief the new agent fully: it may not have access to this conversation. If startup fails, inspect the tab left open by the command rather than assuming an agent is running.

If the agent needs input, relay its question to the user and send the user's answer with `context-drop send TARGET "answer" --wait`. Do not interrupt or close an existing agent without permission.

## Upload a temporary file

```bash
context-drop upload --json --ttl 1h ./artifact.png
```

Set up the server endpoint and token first; see [server setup](../../docs/server.md). Anyone with a link can download the file until it expires. Use the shortest useful TTL and do not upload credentials or private data without permission.
