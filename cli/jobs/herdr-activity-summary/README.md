# Herdr activity journal

A silent LaunchAgent samples Herdr every five minutes, from 09:00 through 20:55 in `America/Los_Angeles`. Only meaningful activity produces a SQLite summary row. It never sends an iMessage or changes Herdr panes.

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

Copy `summarize.py` to `~/.context-drop/managed/state/herdr-activity/summarize.py`, then load the LaunchAgent (every five minutes, 09:00 through 20:55 local time):

```sh
cp jobs/herdr-activity-summary/com.avyay.herdr-activity-summary.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.avyay.herdr-activity-summary.plist
```

It is silent: output goes to `~/Library/Logs/context-drop/herdr-activity-summary.log`. The script also enforces the local-time window, including on manual runs. It does not backfill missed intervals.

## Test

```sh
python3 -m unittest discover -s jobs/herdr-activity-summary -p 'test_*.py'
```
