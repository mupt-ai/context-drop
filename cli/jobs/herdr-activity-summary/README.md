# Herdr activity journal

A silent command schedule samples Herdr every five minutes, from 09:00 through 20:55 in `America/Los_Angeles`. Only meaningful activity produces a SQLite summary row. It never sends an iMessage or changes Herdr panes.

Uses Python 3.9+ standard library, the compatible Herdr CLI, and the existing local Ollama-compatible `qwen3.8:27b-mlx` model on port 11435. No Python dependencies, remote model calls, or model downloads.

## What gets recorded

- All panes on the configured Herdr socket are observed read-only. Recent text, agent status, workspace/tab labels, and current focus are compared with the previous snapshot.
- Only new/changed evidence reaches the model. Focus is a sample, not proof of human activity; agent output can be background work. This is not an exact keystroke/event log.
- Unchanged snapshots skip inference and write no activity row. The model can return a structured null decision for noise such as redraws or automatic tab renaming.
- First run and gaps longer than ten minutes establish a new baseline without a summary. Overnight work is not misattributed to the first morning interval.
- Timestamps are UTC and describe actual observation windows; scheduler polling means these are approximately five minutes, not exact wall-clock event boundaries. Duplicate executions in a five-minute bucket are ignored.
- `activity_summaries` is append-only. `snapshot` retains just the latest bounded terminal snapshot so the next run can calculate changes. Both are private local data, stored with owner-only permissions by default. Terminal content is sent only to the configured local model, not a cloud service.
- Failures exit nonzero without advancing the snapshot. Exclusive process locking prevents overlapping inference/writes, and summary/checkpoint updates share a transaction.

Default database: `~/.context-drop/managed/state/herdr-activity/activity.sqlite3`.

```sql
SELECT window_start, window_end, summary
FROM activity_summaries
ORDER BY id DESC LIMIT 20;
```

## Install

Copy `summarize.py` to `~/.context-drop/managed/state/herdr-activity/summarize.py`, then configure the command with the existing socket and compatible local client:

```sh
context-drop schedule add \
  --name herdr-activity-summary --type command \
  --cron '*/5 9-20 * * *' --timezone America/Los_Angeles \
  --cwd "$HOME" --timeout 4m --retries 0 \
  --command /usr/bin/env --command HERDR_ENV=1 \
  --command /opt/homebrew/bin/python3 \
  --command "$HOME/.context-drop/managed/state/herdr-activity/summarize.py" \
  --command=--herdr --command "$HOME/.local/bin/herdr"
```

No `--notify`: this schedule is silent. The script also enforces the local-time window, including on manual runs. It does not backfill missed intervals.

## Test

```sh
python3 -m unittest discover -s workflows/herdr-activity-summary -p 'test_*.py'
```
