#!/usr/bin/env bash
# Smoke-test the release layout without downloading a release.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'kill "${pid:-}" 2>/dev/null || true; pkill -f "$tmp/lib/context-drop/runtime" 2>/dev/null || true; rm -rf "$tmp"' EXIT
(cd "$root/runtime" && npm run build >/dev/null)
mkdir -p "$tmp/bin" "$tmp/lib/context-drop/runtime"
go build -o "$tmp/bin/context-drop" "$root/cmd/context-drop"
cp -R "$root/runtime/dist" "$tmp/lib/context-drop/runtime/dist"
port=$((50000 + RANDOM % 10000))
CONTEXT_DROP_HOME="$tmp/home" CONTEXT_DROP_RUNTIME_PORT="$port" "$tmp/bin/context-drop" init --machine-name smoke >/dev/null 2>&1 || true
sed -i.bak "s/\"port\": 47762/\"port\": $port/" "$tmp/home/runtime/config.json"
CONTEXT_DROP_HOME="$tmp/home" CONTEXT_DROP_RUNTIME_PORT="$port" "$tmp/bin/context-drop" runtime serve >"$tmp/runtime.log" 2>&1 &
pid=$!
for _ in $(seq 1 30); do
  if CONTEXT_DROP_HOME="$tmp/home" CONTEXT_DROP_RUNTIME_ADDRESS="http://127.0.0.1:$port" "$tmp/bin/context-drop" runtime status >/dev/null 2>&1; then
    echo "installed runtime smoke passed"
    exit 0
  fi
  sleep .1
done
cat "$tmp/runtime.log" >&2
exit 1
