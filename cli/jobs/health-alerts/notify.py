#!/usr/bin/env python3
"""Rate-limit the dashboard updater's failure alerts without stopping updates."""
import fcntl
import os
from pathlib import Path
import subprocess
import sys
import time


def notify(args, state, messenger, now=None):
    now = time.time() if now is None else now
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (state / "failure-alert.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        stamp = state / "failure-alert-at"
        if stamp.exists() and now - float(stamp.read_text()) < 86400:
            print("Dashboard failure alert suppressed (24-hour cooldown).")
            return 0
        # Reserve before sending: an ambiguous failure must not cause duplicates.
        stamp.write_text(str(now))
        stamp.chmod(0o600)
        return subprocess.run([messenger, *args], timeout=30, check=False).returncode


if __name__ == "__main__":
    directory = Path(os.environ.get("HEALTH_DASHBOARD_STATE_DIR", str(Path.home() / ".context-drop/managed/state/health-dashboard")))
    sys.exit(notify(sys.argv[1:], directory, "/opt/homebrew/bin/imsg"))
