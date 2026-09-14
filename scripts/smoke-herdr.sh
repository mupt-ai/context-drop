#!/usr/bin/env sh
# Test double only. It answers the Herdr calls the runtime makes while warming
# the pool, never launches an agent, and never controls a terminal.
set -eu
counter="${CONTEXT_DROP_HOME:?}/smoke-pane-counter"
# Skip the global "--session <name>" prefix.
while [ $# -gt 0 ] && [ "$1" = "--session" ]; do shift 2; done
case "$1 $2" in
  "workspace list") printf '{"result":{"workspaces":[{"workspace_id":"w1","label":"ContextDropManaged"}]}}\n' ;;
  "workspace create"|"tab create")
    count=0
    if [ -f "$counter" ]; then count="$(cat "$counter")"; fi
    count=$((count + 1))
    printf '%s' "$count" > "$counter"
    printf '{"result":{"root_pane":{"pane_id":"w1:p%s"}}}\n' "$count"
    ;;
  "pane run") printf '{}\n' ;;
  "pane list") printf '{"result":{"panes":[{"pane_id":"w1:p1"},{"pane_id":"w1:p2"},{"pane_id":"w1:p3"},{"pane_id":"w1:p4"}]}}\n' ;;
  "pane close") printf '{}\n' ;;
  "agent get") printf '{"result":{"agent":{"agent":"%s","agent_status":"idle","state_change_seq":1,"pane_id":"%s"}}}\n' "${CONTEXT_DROP_SMOKE_AGENT:-codex}" "$3" ;;
  *) exit 1 ;;
esac
