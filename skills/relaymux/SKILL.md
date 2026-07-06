---
name: relaymux
description: Use relaymux directly or as a base layer for skills that launch, monitor, notify, schedule, or steer local CLI agents in tmux. Lightweight reference for relaymux's mental model and core commands.
---

# relaymux

Use this skill when working with relaymux itself or when another skill needs to delegate work through relaymux.

## Mental Model

relaymux coordinates local CLI agents in tmux windows. It does not use tmux panes. A background daemon/local API can run outside tmux when installed, while launched agents stay visible in tmux so a human can attach, inspect, interrupt, or recover them.

By default, relaymux uses one shared tmux session. Pass `--session` only when you intentionally want a separate or named tmux session. Managed config, state, logs, prompts, scratch files, reports, research files, and the SQLite database live under `~/.relaymux` by default.

`relaymux status` is the source of truth for relaymux-launched runs.

## Core Commands

Run setup and health checks with:

```bash
relaymux setup
relaymux status
relaymux doctor
relaymux status-launch-agent --json
```

Launch an agent in a repository:

```bash
relaymux launch \
  --repo <repo-path> \
  --agent <pi|codex|claude|other> \
  --name <short-window-name> \
  --prompt '<specific task, constraints, validation, reporting instructions>'
```

For long or high-stakes prompts, write the prompt to a file and pass it with `--prompt-file`:

```bash
relaymux launch \
  --repo <repo-path> \
  --agent <agent> \
  --name <short-window-name> \
  --prompt-file /tmp/<task>-prompt.md
```

Launch in a git worktree when work should be isolated:

```bash
relaymux launch \
  --repo <source-repo> \
  --worktree <worktree-path> \
  --create-worktree \
  --worktree-branch <branch-name> \
  --worktree-from origin/main \
  --agent <agent> \
  --name <short-window-name> \
  --prompt-file /tmp/<task>-prompt.md
```

Send a one-off request to the relaymux daemon:

```bash
relaymux ask '<request>' --reply-mode none
relaymux ask '<request>' --no-wait --reply-mode imessage
```

Report completion or status from a subagent:

```bash
relaymux notify \
  --from <subagent-name> \
  --reply-mode imessage \
  --idempotency-key '<stable-task-key>-done' \
  --message 'Done: summary. Validation: commands/results. Blockers: none or list.'
```

Use `--reply-mode none` for quiet context-only updates. Use `--suicide` only when the current tmux window is disposable and the task policy permits closing it.

Schedule recurring relaymux requests:

```bash
relaymux schedule add \
  --name <name> \
  --prompt '<prompt>' \
  --cron '*/15 * * * *' \
  --reply-mode none

relaymux schedule list
relaymux schedule remove --name <name>
```

## Delegated Prompt Shape

Make delegated prompts self-contained. Include the repo or worktree path, exact scope, non-goals, files or areas to inspect first when known, instructions to read local repo guidance before editing, validation commands, and the expected final `relaymux notify` command.

Prefer prompt files for multi-line instructions so shell quoting does not corrupt the task.

## Steering Existing Runs

If a follow-up belongs to an existing relaymux run, steer that run instead of launching a duplicate. Do not paste multi-line steering text directly into interactive TUIs that split input by newline. Write the instruction to a temp file and send one single-line steering message such as:

```text
Read and apply /tmp/<task>-followup.md
```

If a run is already confused by fragmented steering, stop and restart only when the task policy allows it.

## Safety Rules

Do not overwrite, revert, or race another agent's work. Do not close or kill relaymux/tmux code-task windows unless the user explicitly asks or the project policy says the work is safe to clean up.

For long-running delegated work, keep a lightweight status check running when useful. Remove scheduled status checks when no active jobs or blockers remain.

Report status concisely: what is running, what is blocked, and what needs human input.
