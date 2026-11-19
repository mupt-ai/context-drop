#!/usr/bin/env bash
# Build the server and deploy it to the Context Drop VM.
#
#   deploy/vm/deploy.sh
#
# Safe to rerun: it installs Caddy the first time, then replaces the binary,
# config, and unit, and restarts the server. Restarting drops every daemon's
# connection; daemons reconnect within seconds.
set -euo pipefail

PROJECT=${PROJECT:-context-drop}
ZONE=${ZONE:-us-central1-a}
VM=${VM:-context-drop}
HOSTS=${HOSTS:-contextdrop.dev}

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

(cd "$here/../../cli" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="-s -w" -o "$work/context-drop-server" ./cmd/context-drop-server)
cp "$here/context-drop.service" "$work/"
sed "s|@HOSTS@|$HOSTS|" "$here/Caddyfile" >"$work/Caddyfile"
{
	cat "$here/env"
	printf 'CONTEXT_DROP_ADMIN_TOKEN=%s\n' \
		"$(gcloud secrets versions access latest --secret context-drop-admin-token --project "$PROJECT")"
} >"$work/env"
chmod 600 "$work/env"

gcloud compute scp --project "$PROJECT" --zone "$ZONE" --tunnel-through-iap \
	"$work/context-drop-server" "$work/context-drop.service" "$work/Caddyfile" "$work/env" "$VM:/tmp/"
gcloud compute ssh --project "$PROJECT" --zone "$ZONE" --tunnel-through-iap "$VM" -- sudo bash -s <<'REMOTE'
set -euo pipefail
command -v caddy >/dev/null || { DEBIAN_FRONTEND=noninteractive apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -yq caddy; }
# Mods run in Deno.
DENO_VERSION=v2.9.5
if [ "$(/usr/local/bin/deno --version 2>/dev/null | head -1 | cut -d' ' -f2)" != "${DENO_VERSION#v}" ]; then
	command -v unzip >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -yq unzip
	curl -fsSL -o /tmp/deno.zip "https://github.com/denoland/deno/releases/download/$DENO_VERSION/deno-x86_64-unknown-linux-gnu.zip"
	unzip -o -q /tmp/deno.zip -d /usr/local/bin && rm -f /tmp/deno.zip
fi
install -m 755 /tmp/context-drop-server /usr/local/bin/context-drop-server
install -D -m 600 /tmp/env /etc/context-drop/env
install -m 644 /tmp/context-drop.service /etc/systemd/system/context-drop.service
install -m 644 /tmp/Caddyfile /etc/caddy/Caddyfile
rm -f /tmp/context-drop-server /tmp/env /tmp/context-drop.service /tmp/Caddyfile
systemctl daemon-reload
systemctl enable -q context-drop caddy
systemctl restart context-drop
systemctl reload caddy || systemctl restart caddy
sleep 1
systemctl is-active context-drop caddy
curl -fsS http://127.0.0.1:8080/healthz
REMOTE
