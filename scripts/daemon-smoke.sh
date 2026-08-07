#!/usr/bin/env bash
# Isolated Context Drop daemon lifecycle smoke test.
# Uses a private CONTEXT_DROP_HOME and a non-default runtime port so it never
# touches the real Context Drop home, a live daemon, or tmux. It launches the
# runtime supervised by the daemon but never launches a coding agent.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/bin/context-drop"
if [[ ! -x "$bin" ]]; then
  echo "context-drop binary missing; run: make build-cli" >&2
  exit 1
fi
if [[ ! -f "$root/runtime/dist/src/main.js" ]]; then
  echo "runtime assets missing; run: make runtime-build" >&2
  exit 1
fi

home="$(mktemp -d "${TMPDIR:-/tmp}/context-drop-daemon-smoke.XXXXXX")"
port=$((48000 + ($$ % 1000)))
daemon_pid=""
cleanup() {
  if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  rm -rf "$home"
}
trap cleanup EXIT

export CONTEXT_DROP_HOME="$home"
export CONTEXT_DROP_RUNTIME_PORT="$port"

echo "starting isolated daemon (home=$home port=$port)"
"$bin" daemon run >"$home/test-daemon.log" 2>&1 &
daemon_pid=$!
pid_file="$home/daemon/daemon.pid"
read_pid() {
  if [[ ! -f "$pid_file" ]]; then
    return 1
  fi
  local value
  value="$(grep -oE '"pid":[0-9]+' "$pid_file" | grep -oE '[0-9]+' || true)"
  if [[ -z "$value" ]]; then
    return 1
  fi
  printf '%s' "$value"
}
pid=""
for _ in $(seq 1 30); do
  pid="$(read_pid || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    break
  fi
  pid=""
  sleep 0.2
done
if [[ -z "$pid" ]] || ! kill -0 "$pid" 2>/dev/null; then
  echo "daemon did not start" >&2
  cat "$home/daemon/daemon.log" 2>/dev/null >&2 || true
  exit 1
fi
echo "daemon pid $pid"

healthy=""
for _ in $(seq 1 40); do
  status="$("$bin" daemon status --json 2>/dev/null || true)"
  alive="$(printf '%s' "$status" | grep -oE '"alive":(true|false)' | grep -oE '(true|false)' || true)"
  runtime_ok="$(printf '%s' "$status" | grep -oE '"runtime_healthy":(true|false)' | grep -oE '(true|false)' || true)"
  if [[ "$alive" == "true" && "$runtime_ok" == "true" ]]; then
    healthy="yes"
    break
  fi
  sleep 0.5
done
if [[ "$healthy" != "yes" ]]; then
  echo "daemon/runtime did not become healthy" >&2
  cat "$home/daemon/daemon.log" 2>/dev/null >&2 || true
  exit 1
fi
echo "daemon and runtime healthy"

kill -TERM "$daemon_pid"
wait "$daemon_pid" || true
daemon_pid=""
pid=""
for _ in $(seq 1 30); do
  if [[ ! -f "$pid_file" ]]; then
    break
  fi
  sleep 0.2
done
if [[ -f "$pid_file" ]]; then
  echo "daemon PID file was not removed after stop" >&2
  exit 1
fi
echo "isolated daemon smoke passed"
