---
name: context-drop
description: Use the Context Drop CLI to list, read, message, and summarize live coding-agent sessions (Claude Code, Codex, Pi, OpenCode, Muse, and their subagents) on this machine, or upload temporary files with expiring links. Use when asked what other agents are doing, to check on or message another agent session, or to share a temporary file.
---

# Context Drop

`context-drop` indexes every live Claude Code, Codex, Pi, OpenCode, and Muse session on this machine, including subagents. Each session has a stable ID such as `cc-6a79295a` (Claude Code), `cx-…` (Codex), `pi-…`, `oc-…` (OpenCode), or `mu-…` (Muse).

## Find a session

```bash
context-drop list            # add --json for native IDs, PIDs, transcripts, delivery channel
context-drop read TARGET     # recent transcript; -n 50 for more
```

A target is an ID, a native session ID, a unique prefix, or a session name. Choose by working directory, name, or transcript. If several sessions could match the user's description, ask which one.

Statuses: `working` is mid-turn; `idle` is ready; `blocked` is waiting for an approval or answer; `unknown` means the agent's state could not be read.

## Message a session

```bash
context-drop send TARGET "what to do next" --wait
```

The message arrives as the session's next prompt (queued if it is busy). `--wait` prints the reply once the agent settles. Brief the other agent fully; it cannot see this conversation. Do not message a session the user did not ask you to involve, and ask before sending into a session that is `working` on something unrelated. Read and report the agent's actual reply; sending is not proof of finished work.

Claude Code and Muse subagents are reached through their parent session, which is asked to forward the message. If `send` reports that a Pi session has no delivery channel, the user must run `context-drop setup pi` and restart that Pi session.

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
