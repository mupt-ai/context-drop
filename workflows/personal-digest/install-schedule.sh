#!/usr/bin/env bash
# Idempotently upsert the personal-digest schedule with a short workflow prompt.
# Existing cadence, agent, repository, backend, and enabled state are preserved.
# This command only updates daemon state; it never launches a schedule run.
set -euo pipefail

name="personal-digest"
default_agent="pi"
default_repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
default_every="1h"
every=""
cron=""
timezone=""

while (($#)); do
  case "$1" in
    --every) every=${2:?missing duration}; shift 2 ;;
    --cron) cron=${2:?missing expression}; shift 2 ;;
    --timezone) timezone=${2:?missing timezone}; shift 2 ;;
    *) printf 'unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done
if [[ -n "$every" && -n "$cron" ]]; then
  echo "use either --every or --cron" >&2
  exit 2
fi

existing_json=$(context-drop schedule list --json)
existing_fields=$(node -e '
const fs=require("fs");
const data=JSON.parse(fs.readFileSync(0,"utf8"));
const s=(data.schedules||[]).find(s=>s.name===process.argv[1]);
if (s) process.stdout.write([s.agent||"",s.repo||"",s.backend||"",String(s.every||0),s.cron||"",s.timezone||"",String(s.enabled!==false)].join("|"));
' "$name" <<<"$existing_json")

agent=$default_agent
repo=$default_repo
backend=""
existing_every_ns=0
existing_cron=""
existing_timezone=""
enabled=true
if [[ -n "$existing_fields" ]]; then
  IFS='|' read -r agent repo backend existing_every_ns existing_cron existing_timezone enabled <<<"$existing_fields"
  agent=${agent:-$default_agent}
  repo=${repo:-$default_repo}
fi

args=(schedule add --name "$name" --type agent --agent "$agent" --repo "$repo")
if [[ -n "$backend" ]]; then args+=(--backend "$backend"); fi
if [[ "$enabled" == false ]]; then args+=(--disabled); fi

if [[ -n "$cron" ]]; then
  args+=(--cron "$cron" --timezone "${timezone:-America/Los_Angeles}")
elif [[ -n "$every" ]]; then
  args+=(--every "$every")
elif [[ -n "$existing_cron" ]]; then
  args+=(--cron "$existing_cron" --timezone "${existing_timezone:-America/Los_Angeles}")
elif ((existing_every_ns > 0)); then
  # Go encodes time.Duration as integer nanoseconds; CLI accepts an exact ns duration.
  args+=(--every "${existing_every_ns}ns")
else
  args+=(--every "$default_every")
fi

prompt="Read and execute workflows/personal-digest/WORKFLOW.md in this repository. It is the complete workflow specification; follow it fully."
args+=(--prompt "$prompt")

printf 'Upserting %s without launching work.\n' "$name"
exec context-drop "${args[@]}"
