# Dashboard failure alert cooldown

`notify.py` wraps the health updater's existing `imsg send` call. It forwards the original arguments at most once per 24 hours; updates and their detailed logs continue normally. The timestamp is reserved before sending to avoid retrying an ambiguous delivery. A file lock prevents concurrent sends. Success does not reset the cooldown, avoiding flapping alerts.

Install the executable into the private dashboard state directory and set the `com.avyay.health-pages-update` LaunchAgent's `HEALTH_PAGES_IMSG_BIN` environment variable to its absolute path. Reload that LaunchAgent to apply. The existing updater already supports this override; no dirty health source files need changing.

State defaults to `~/.context-drop/managed/state/health-dashboard`, overridable via `HEALTH_DASHBOARD_STATE_DIR`. Messages still use `/opt/homebrew/bin/imsg`. Remove the environment override and reload the LaunchAgent to roll back.

Tests (mock transport; no real messages):

```
python3 -m unittest discover -s workflows/health-alerts
```
