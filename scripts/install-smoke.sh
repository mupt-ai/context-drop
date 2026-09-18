#!/usr/bin/env bash
# Verify an installed daemon/runtime layout with an isolated home and a fake Herdr.
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
cp "$root/scripts/smoke-herdr.sh" "$task_tmp/fake-bin/herdr"
chmod +x "$task_tmp/fake-bin/herdr"
printf '#!/bin/sh\nexit 1\n' > "$task_tmp/fake-bin/codex"
chmod +x "$task_tmp/fake-bin/codex"
cp "$task_tmp/fake-bin/codex" "$task_tmp/fake-bin/dari"
port=$((50000 + RANDOM % 10000))
export CONTEXT_DROP_HOME="$task_tmp/home"
export CONTEXT_DROP_RUNTIME_PORT="$port"
export PATH="$task_tmp/fake-bin:$PATH"
"$task_tmp/bin/context-drop" daemon run >"$task_tmp/runtime.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 60); do
  status="$("$task_tmp/bin/context-drop" daemon status --json 2>/dev/null || true)"
  if [[ "$status" == *'"runtime_healthy":true'* ]]; then
    # Verify the installed runtime supports the tools embedded in the CLI.
    # This is an isolated home/runtime: issuing its capability cannot affect the live router.
    node --input-type=module - "$CONTEXT_DROP_HOME" "$port" <<'JS'
import { readFileSync } from "node:fs";
const [home, port] = process.argv.slice(2);
const config = JSON.parse(readFileSync(`${home}/runtime/config.json`, "utf8"));
const token = readFileSync(config.tokenFile, "utf8").trim();
const base = `http://127.0.0.1:${port}`;
const auth = await fetch(`${base}/v1/router-capabilities`, {
  method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
  body: JSON.stringify({ routerId: "install-smoke", chatId: "fixture" }),
});
if (!auth.ok) throw new Error(`router capability: ${auth.status}`);
const { capability } = await auth.json();
const headers = { authorization: `Bearer ${capability}`, "content-type": "application/json" };
const discovery = await fetch(`${base}/v1/workspaces`, { headers });
if (!discovery.ok || !Array.isArray((await discovery.json()).workspaces)) throw new Error("installed workspace discovery is unavailable");
const invalid = await fetch(`${base}/v1/workspaces/delegate`, {
  method: "POST", headers, body: JSON.stringify({ workspace: "missing", mode: "new", worker: 1, prompt: "fixture" }),
});
if (invalid.status !== 400) throw new Error(`installed delegation route: ${invalid.status}, expected validation failure`);
JS
    echo "installed daemon/runtime and workspace endpoints smoke passed"
    exit 0
  fi
  sleep .1
done
cat "$task_tmp/runtime.log" >&2
exit 1
