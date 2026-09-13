#!/usr/bin/env sh
# Test double only. It never launches an agent or controls a terminal.
set -eu
counter="${CONTEXT_DROP_HOME:?}/smoke-pane-counter"
case "$1" in
  has-session) exit 1 ;;
  new-session|new-window)
    count=0
    if [ -f "$counter" ]; then count="$(cat "$counter")"; fi
    count=$((count + 1))
    printf '%s' "$count" > "$counter"
    printf '%%%s\n' "$count"
    ;;
  list-panes) printf '%%1\n%%2\n%%3\n%%4\n' ;;
  *) exit 1 ;;
esac
