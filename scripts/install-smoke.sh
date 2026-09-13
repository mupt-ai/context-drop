#!/usr/bin/env bash
# Verify an installed daemon/runtime layout with an isolated home and fake tmux.
# Real native Codex behavior is tested separately against a local model fixture.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
task_tmp="$(mktemp -d)"
daemon_pid=""
cleanup() {
  if [[ -n "$daemon_pid" ]]; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  rm -rf "$task_tmp"
}
trap cleanup EXIT
(cd "$root/runtime" && npm run build >/dev/null)
mkdir -p "$task_tmp/bin" "$task_tmp/lib/context-drop/runtime" "$task_tmp/fake-bin"
go build -o "$task_tmp/bin/context-drop" "$root/cmd/context-drop"
cp -R "$root/runtime/dist" "$task_tmp/lib/context-drop/runtime/dist"
cp "$root/runtime/package.json" "$root/runtime/package-lock.json" "$task_tmp/lib/context-drop/runtime/"
(cd "$task_tmp/lib/context-drop/runtime" && npm ci --omit=dev --workspaces=false >/dev/null)
cp "$root/scripts/smoke-tmux.sh" "$task_tmp/fake-bin/tmux"
chmod +x "$task_tmp/fake-bin/tmux"
printf '#!/bin/sh\nexit 1\n' > "$task_tmp/fake-bin/codex"
chmod +x "$task_tmp/fake-bin/codex"
cp "$task_tmp/fake-bin/codex" "$task_tmp/fake-bin/dari"
port=$((50000 + RANDOM % 10000))
export CONTEXT_DROP_HOME="$task_tmp/home"
export CONTEXT_DROP_RUNTIME_PORT="$port"
export CONTEXT_DROP_BACKEND=tmux
export PATH="$task_tmp/fake-bin:$PATH"
"$task_tmp/bin/context-drop" daemon run >"$task_tmp/runtime.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 60); do
  status="$("$task_tmp/bin/context-drop" daemon status --json 2>/dev/null || true)"
  if [[ "$status" == *'"runtime_healthy":true'* ]]; then
    echo "installed daemon/runtime smoke passed"
    exit 0
  fi
  sleep .1
done
cat "$task_tmp/runtime.log" >&2
exit 1
