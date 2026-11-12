---
name: context-drop
description: Use the Context Drop CLI to list, read, message, and summarize live coding-agent sessions (Claude Code, Codex, Pi, OpenCode, and Claude Code subagents) on this machine, or upload temporary files with expiring links. Use when asked what other agents are doing, to check on or message another agent session, or to share a temporary file.
---

# Context Drop

`context-drop` indexes every live Claude Code, Codex, Pi, and OpenCode session on this machine, including Claude Code subagents. Each session has a stable ID such as `cc-6a79295a` (Claude Code), `cx-…` (Codex), `pi-…`, or `oc-…` (OpenCode).

## Find a session

```bash
context-drop list            # add --json for native IDs, PIDs, transcripts, delivery channel, location
context-drop list --in rex --session calm-prairie   # only agents in that place (also --window, --pane)
context-drop read TARGET     # recent transcript; -n 50 for more
context-drop read TARGET --screen   # the agent's terminal pane, including prompts and dialogs
```

A target is an ID, a native session ID, a unique prefix, or a session name. Choose by working directory, name, or transcript. If several sessions could match the user's description, ask which one.

Statuses: `working` is mid-turn; `idle` is ready; `blocked` is waiting for an approval or answer; `unknown` means the agent's state could not be read.

## Message a session

```bash
context-drop send TARGET "what to do next" --wait
```

The message arrives as the session's next prompt (queued if it is busy). `--wait` prints the reply once the agent settles. Brief the other agent fully; it cannot see this conversation. Do not message a session the user did not ask you to involve, and ask before sending into a session that is `working` on something unrelated. Read and report the agent's actual reply; sending is not proof of finished work.

Claude Code subagents are reached through their parent session, which is asked to forward the message. If `send` reports that a Pi session has no delivery channel (it is not in a tmux, herdr, or Rex pane and has no Context Drop extension), the user must run `context-drop setup pi` and restart that Pi session.

## Coordinate with other agents

```bash
context-drop overlaps                         # who shares your worktree or has edited the same files
context-drop claims                           # what other agents have claimed
context-drop claim path/or/task --note "why"  # before working on something others might touch
context-drop release                          # when you are done
context-drop watch --for 10m --json           # status changes as they happen
```

Before editing in a shared repository, check `overlaps` and `claims`. If someone else holds a claim on what you need, coordinate with them (`send`) instead of passing `--force`. Claims are advisory and disappear when your session ends; release them when you finish early. `send --all` (with `--in`, `--session`, `--agent`, and similar filters) messages a whole group and skips you; use it only when the user asked to involve those sessions. Do not `stop` sessions the user did not ask you to stop.

## Start an agent

```bash
context-drop start claude --cwd /absolute/path --prompt "the task and relevant context" --wait
```

Opens the agent in a tmux window, herdr tab, or Rex block (`--in`, default from config or the one you are in) and prints its ID. To put it somewhere specific, add `--session` (a Rex or tmux session, a herdr workspace) and optionally `--window` or `--pane` (a Rex block) to split beside it, e.g. `--in rex --session calm-prairie`. Brief it fully in `--prompt`. If the agent stops at a folder-trust or login prompt, `start` reports it (`waiting_for_trust`, `waiting_for_setup`) and does not send the prompt; tell the user, or pass `--trust` only if the user agreed to trust that folder. Ask before starting agents the user did not request.

## Summarize everything

```bash
context-drop summarize
```

Summarizes each live session with the configured model and only re-summarizes sessions whose transcripts changed since the last run. Transcript excerpts go to that model, so prefer the user's configured or a local provider (`--provider ollama --model …`).

## Upload a temporary file

```bash
context-drop upload --json --ttl 1h ./artifact.png
```

Set up the server endpoint and token first; see [server setup](../../docs/server.md). Anyone with a link can download the file until it expires. Use the shortest useful TTL and do not upload credentials or private data without permission.
