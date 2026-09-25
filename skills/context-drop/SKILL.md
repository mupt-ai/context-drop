---
name: context-drop
description: Talk to Avyay's coding agents running in Herdr (list them, check what one is doing, continue one, start a new one in a worktree, wait for results) and upload temporary files with expiring links. Use whenever a request concerns his actual coding work or a named agent/tab/worktree.
---

# Context Drop

`context-drop` drives coding agents that run in Herdr tabs, and uploads files to an expiring link store. Every command that acts on an agent takes an exact TARGET: its herdr name or pane ID from `context-drop agents`. Nothing falls back to the focused pane.

## See what is running

```bash
context-drop agents            # TARGET  KIND  STATUS  TITLE  CWD
context-drop read TARGET       # recent terminal output
```

Statuses: `working` is mid-turn; `idle` and `done` are ready for input (`done` means it finished while nobody was looking); `blocked` is waiting on an approval or question; `unknown` means herdr cannot tell.

Pick the target by name, tab title, or cwd. If more than one agent plausibly matches, ask which one. If nothing matches, say so. Never guess.

## Continue an existing agent

```bash
context-drop send TARGET "what to do next" --wait
```

Run it as a background command with no exec timeout (in OpenClaw: `background: true, timeoutSeconds: 0`; context-drop enforces its own six-hour limit). When it returns, it prints the agent's status and output; relay what the agent actually did or asked. `send` refuses a `working`, `blocked`, or `unknown` agent. Tell Avyay it is busy and offer to queue it; only pass `--force` when he says to. Continuing an agent keeps its conversation and worktree: never send `/clear` or `/new`, restart, close, or move it.

If an agent is already `working` and you just need to know when it finishes, run `context-drop wait TARGET` in the background.

## Start a new agent

1. Create an isolated worktree with the house tooling, from the repo's main checkout:
   ```bash
   cd REPO && git checkout main && git pull --ff-only && zsh -ic '_gwt_create BRANCH'
   ```
   The worktree lands in `~/.avyay-worktrees/BRANCH`. If checkout or pull fails because main has local changes, stop and tell Avyay; do not stash or reset.
2. Start the agent in a tab of the right herdr workspace (labels come from the workspace the repo already lives in, e.g. `dari-mono`):
   ```bash
   context-drop new NAME "the task, with the context it needs" \
     --workspace LABEL --cwd ~/.avyay-worktrees/BRANCH --agent claude --wait
   ```
   `NAME` is a short lowercase slug (letters, digits, `-`, `_`). Default agent: Pi for normal coding, Codex for hard architecture, Claude for frontend/design, or whatever Avyay asks for.

Brief the new agent fully: it has none of this conversation. Include the goal, constraints, and what "done" means.

## When an agent needs input

If a wait ends with the agent `blocked`, or `idle` with a question in its output, ask Avyay that question plainly. Send his answer back with `context-drop send TARGET "answer" --wait`. For an approval menu, read the screen and send the choice he picked.

## Rules

- Never stop, close, restart, or interrupt an agent unless Avyay explicitly asks.
- A started or prompted agent is not finished work. Report results only from output you have read.
- Keep agent output out of chat unless it matters; summarize what changed and what is left.

## Upload a temporary file

```bash
context-drop upload --json --ttl 1h ./artifact.png
```

Links are public until they expire; use the shortest useful TTL. Never upload credentials, `.env` files, private keys, or private data without explicit approval. When sharing a link, say what it is and when it expires.
