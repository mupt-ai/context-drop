#!/usr/bin/env bash
# Smoke-test the release layout without downloading a release.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
cleanup() {
  if [[ -n "${pid:-}" ]]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  pkill -f "$tmp/lib/context-drop/runtime" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT
(cd "$root/runtime" && npm run build >/dev/null)
mkdir -p "$tmp/bin" "$tmp/lib/context-drop/runtime"
go build -o "$tmp/bin/context-drop" "$root/cmd/context-drop"
cp -R "$root/runtime/dist" "$tmp/lib/context-drop/runtime/dist"
port=$((50000 + RANDOM % 10000))
CONTEXT_DROP_HOME="$tmp/home" CONTEXT_DROP_RUNTIME_PORT="$port" "$tmp/bin/context-drop" daemon run >"$tmp/daemon.log" 2>&1 &
pid=$!
for _ in $(seq 1 50); do
  if CONTEXT_DROP_HOME="$tmp/home" CONTEXT_DROP_RUNTIME_ADDRESS="http://127.0.0.1:$port" "$tmp/bin/context-drop" daemon status >/dev/null 2>&1; then
    echo "installed daemon/runtime smoke passed"
    exit 0
  fi
  sleep .1
done
cat "$tmp/daemon.log" >&2
exit 1
