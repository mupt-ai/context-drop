#!/usr/bin/env bash
# Fake-only iMessage daemon smoke. Never touches Messages.app or a real agent.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/bin/context-drop"
[[ -x "$bin" ]] || { echo "run make build-cli first" >&2; exit 1; }
[[ -f "$root/runtime/dist/src/main.js" ]] || { echo "run make runtime-build first" >&2; exit 1; }
home="$(mktemp -d "${TMPDIR:-/tmp}/context-drop-imessage-smoke.XXXXXX")"
port=$((50000 + ($$ % 1000)))
export CONTEXT_DROP_HOME="$home" CONTEXT_DROP_RUNTIME_PORT="$port" FAKE_IMSG_HISTORY="$home/history.json" FAKE_IMSG_SENDS="$home/sends.jsonl"
daemon_pid=""
cleanup() {
  if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  rm -rf "$home"
}
trap cleanup EXIT
cat >"$home/fake-imsg" <<'SCRIPT'
#!/bin/sh
set -eu
case "$1" in
  history) cat "$FAKE_IMSG_HISTORY" ;;
  send)
    [ "$2" = "--chat-id" ] && [ "$4" = "--text" ]
    printf '{"chat":%s,"text":%s}\n' "$(printf '%s' "$3" | sed 's/.*/"&"/')" "$(printf '%s' "$5" | sed 's/.*/"&"/')" >> "$FAKE_IMSG_SENDS"
    printf '{"ok":true}\n'
    ;;
  *) exit 2 ;;
esac
SCRIPT
cat >"$home/fake-responder" <<'SCRIPT'
#!/bin/sh
set -eu
[ -f "$1" ]
printf 'FAKE_CONTEXT_DROP_REPLY\n'
SCRIPT
chmod 700 "$home/fake-imsg" "$home/fake-responder"
printf '[{"id":"old","text":"old","chat_id":"smoke-chat","is_from_me":false}]\n' > "$FAKE_IMSG_HISTORY"
"$bin" imessage setup --chat-id smoke-chat --imsg-path "$home/fake-imsg" --responder-arg "$home/fake-responder" --responder-arg '{prompt_file}' --poll 1s >/dev/null
"$bin" daemon run >"$home/test-daemon.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 40); do
  initialized="$("$bin" imessage status --json 2>/dev/null | grep -o '"initialized":true' || true)"
  [[ -n "$initialized" ]] && break
  sleep 0.25
done
[[ -n "${initialized:-}" ]] || { echo "initial sync failed" >&2; exit 1; }
[[ ! -e "$FAKE_IMSG_SENDS" ]] || { echo "initial sync sent a reply" >&2; exit 1; }
printf '[{"id":"old","text":"old","chat_id":"smoke-chat","is_from_me":false},{"id":"new","text":"hello","chat_id":"smoke-chat","is_from_me":false}]\n' > "$FAKE_IMSG_HISTORY"
for _ in $(seq 1 60); do
  [[ -f "$FAKE_IMSG_SENDS" ]] && grep -q FAKE_CONTEXT_DROP_REPLY "$FAKE_IMSG_SENDS" && break
  sleep 0.25
done
[[ -f "$FAKE_IMSG_SENDS" ]] && grep -q FAKE_CONTEXT_DROP_REPLY "$FAKE_IMSG_SENDS" || { echo "fresh message was not answered" >&2; exit 1; }
sleep 2
[[ "$(wc -l < "$FAKE_IMSG_SENDS" | tr -d ' ')" = "1" ]] || { echo "duplicate reply sent" >&2; exit 1; }
echo "fake iMessage smoke passed"
