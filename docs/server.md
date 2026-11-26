# Server and self-hosting

The Context Drop server is a self-hosted TTL file store. It exposes health checks, one optionally authenticated upload endpoint, and opaque public download URLs. It has no chains, machine pairing, inboxes, handoffs, listing, pull, or delete APIs.

## API

- `GET /health` and `GET /healthz` return `ok`.
- `POST /v1/drops` uploads a non-empty body. It accepts anonymous uploads when `CONTEXT_DROP_UPLOAD_TOKEN` is unset; otherwise it requires `Authorization: Bearer <CONTEXT_DROP_UPLOAD_TOKEN>` and accepts `X-Filename`, `Content-Type`, and `X-TTL` headers.
- `GET /d/<opaque-id>` downloads a file until its TTL expires. Anyone with the unguessable URL can open it; expired links return `410 Gone`.

The server retains content-type and size limits. Safe image, PDF, and text types may render inline; other types download as attachments. There is no listing endpoint. Local storage deletes expired drops hourly. On GCS, expired objects stay until a bucket lifecycle rule deletes them (see below); the server warns at startup when it finds none (reading the lifecycle needs `storage.buckets.get`; without it the server warns that it could not check).

## Public hosting

Leave `CONTEXT_DROP_UPLOAD_TOKEN` unset to accept uploads from anyone, including on `https://contextdrop.dev`. Deploy the updated server and remove any existing token from its environment to enable this behavior. Clients need only the endpoint.

Anonymous uploads are limited per client IP: by default 60 uploads and 250 MB per hour (`CONTEXT_DROP_UPLOADS_PER_HOUR`, `CONTEXT_DROP_UPLOAD_BYTES_PER_HOUR`; `0` turns a limit off). Over the limit, uploads get `429` with `Retry-After`. Uploads with the upload token are never limited. The counts live in memory per server instance, so with several instances the effective limit is multiplied; keep ingress rate limits for anything stricter.

Limits are keyed by client IP (by /64 for IPv6). **Behind a proxy you must set `CONTEXT_DROP_PROXY_HOPS`**, or every client shares the proxy's address and one budget. It is the number of proxies that append to `X-Forwarded-For` in front of the server: `1` for Cloud Run alone, `2` behind a Google Cloud HTTPS load balancer (it appends the client and then its own address). The server takes the entry that many places from the right; anything further left is client-supplied. Leave it unset when clients connect directly. An invalid value stops the server from starting.

Link expiry does not delete GCS objects. Add a lifecycle rule that deletes them a day after the maximum TTL (8 days for the default 168h):

```sh
cat > lifecycle.json <<'JSON'
{"rule": [{"action": {"type": "Delete"}, "condition": {"age": 8}}]}
JSON
gcloud storage buckets update gs://your-bucket --lifecycle-file=lifecycle.json
```

Scope it with `"matchesPrefix": ["your-prefix/drops/"]` if the bucket holds anything else.

## Run locally

```sh
CONTEXT_DROP_UPLOAD_TOKEN='replace-with-a-long-random-secret' \
CONTEXT_DROP_STORAGE=local \
CONTEXT_DROP_DATA_DIR=.data \
CONTEXT_DROP_BASE_URL=http://localhost:8080 \
CONTEXT_DROP_ADDR=:8080 \
(cd cli && go run ./cmd/context-drop-server)
```

Configure the same token for uploads:

```sh
export CONTEXT_DROP_ENDPOINT=http://localhost:8080
export CONTEXT_DROP_UPLOAD_TOKEN='replace-with-a-long-random-secret'
context-drop upload ./example.txt
```

## Google Cloud Storage

```sh
CONTEXT_DROP_UPLOAD_TOKEN='replace-with-a-long-random-secret' \
CONTEXT_DROP_STORAGE=gcs \
CONTEXT_DROP_GCS_BUCKET=your-bucket \
CONTEXT_DROP_GCS_PREFIX=context-drop \
CONTEXT_DROP_BASE_URL=https://drop.example.com \
CONTEXT_DROP_ADDR=:8080 \
CONTEXT_DROP_DEFAULT_TTL=24h \
CONTEXT_DROP_MAX_TTL=168h \
CONTEXT_DROP_MAX_BYTES=26214400 \
(cd cli && go run ./cmd/context-drop-server)
```

The GCS backend uses normal Google Cloud authentication. Put production deployments behind HTTPS and pass the upload token through your platform's secret manager. The token is never logged by Context Drop.

## Docker

```sh
docker build -t context-drop-server .
docker run --rm -p 8080:8080 \
  -e CONTEXT_DROP_UPLOAD_TOKEN='replace-with-a-long-random-secret' \
  -e CONTEXT_DROP_STORAGE=local \
  -e CONTEXT_DROP_DATA_DIR=/data \
  -e CONTEXT_DROP_BASE_URL=http://localhost:8080 \
  -v "$PWD/.data:/data" \
  context-drop-server
```

## Server environment

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `CONTEXT_DROP_UPLOAD_TOKEN` | no | — | Optional bearer token for private servers. Unset means public uploads. |
| `CONTEXT_DROP_ADDR` | no | `:8080` | Listen address. |
| `CONTEXT_DROP_BASE_URL` | no | `http://localhost:8080` | Public origin used in returned download URLs. |
| `CONTEXT_DROP_STORAGE` | no | `local` | `local` or `gcs`. |
| `CONTEXT_DROP_DATA_DIR` | no | `.data` | Local storage root. |
| `CONTEXT_DROP_GCS_BUCKET` | for GCS | — | GCS bucket name. |
| `CONTEXT_DROP_GCS_PREFIX` | no | — | Optional GCS object prefix. |
| `CONTEXT_DROP_DEFAULT_TTL` | no | `24h` | Default link lifetime. |
| `CONTEXT_DROP_MAX_TTL` | no | `168h` | Maximum accepted `X-TTL`. |
| `CONTEXT_DROP_MAX_BYTES` | no | `26214400` | Maximum upload size. |
| `CONTEXT_DROP_UPLOADS_PER_HOUR` | no | `60` | Anonymous uploads per client IP per hour; `0` disables. |
| `CONTEXT_DROP_UPLOAD_BYTES_PER_HOUR` | no | `262144000` | Anonymous upload bytes per client IP per hour; `0` disables. |
| `CONTEXT_DROP_PROXY_HOPS` | behind a proxy | `0` | Trusted proxies appending to `X-Forwarded-For`; the client is the entry that many from the right. |
| `CONTEXT_DROP_ADMIN_TOKEN` | to create orgs | unset | Bearer token for `POST /v1/orgs` and `POST /v1/orgs/{org}/join-token`; unset disables both. |
| `CONTEXT_DROP_ORG_STATE` | no | `<data dir>/orgs.json` | File holding orgs and joined machines (credentials are stored as hashes). |
| `CONTEXT_DROP_DENO` | for mods | `deno` on PATH | Deno executable that runs [mods](mods.md); without it, mods are off. |
| `CONTEXT_DROP_MODS_DIR` | no | `<data dir>/mods` | Deployed mods and their key-value data. |
| `CONTEXT_DROP_TOKEN_KEY` | for Slack or Linear | unset | Seals the tokens [extensions](extensions.md) store; unset turns Slack and Linear off, and changing it disconnects every org. `CONTEXT_DROP_SLACK_KEY` is its older name. |
| `CONTEXT_DROP_LINEAR_CLIENT_ID`, `_CLIENT_SECRET`, `_WEBHOOK_SECRET` | for the Linear app | unset | The [Linear](linear.md) OAuth app; without it, orgs connect Linear with an API key. |
| `CONTEXT_DROP_SLACK_CLIENT_ID` | for Slack | unset | The Slack app's client ID; unset turns [Slack](slack.md) off. |
| `CONTEXT_DROP_SLACK_CLIENT_SECRET` | for Slack | — | The Slack app's client secret. |
| `CONTEXT_DROP_SLACK_SIGNING_SECRET` | for Slack | — | Verifies events Slack sends to `/v1/slack/events`. |

## Orgs

The server also hosts orgs: pools of agents across machines. Create one with the admin token; the response holds the join token, which is shown only once:

```sh
curl -X POST -H "Authorization: Bearer $CONTEXT_DROP_ADMIN_TOKEN" -d '{"name":"acme"}' https://ctx.example.com/v1/orgs
# {"org":"acme","join_token":"cdj_…"}
```

On each machine, `context-drop join --server https://ctx.example.com cdj_…`, then `context-drop daemon`. `POST /v1/orgs/acme/join-token` replaces the join token; machines that already joined keep working.

Machines hold a WebSocket to `GET /v1/{org}/connect`; `POST /v1/{org}/list`, `/read`, `/send`, and `/leave` take the machine's credential as a bearer token. Agent lists and relayed calls live in the server's memory, so the org endpoints need a single, always-on instance: Cloud Run's request cap would drop every daemon hourly, and several instances would not share connections.

## contextdrop.dev

`https://contextdrop.dev` runs on one VM, `context-drop` in GCP project `context-drop` (us-central1-a, e2-medium, static IP `130.211.238.184`, daily disk snapshots kept 7 days). Caddy terminates TLS and proxies to the server, which runs as the systemd unit `context-drop`. Drops are stored in the `context-drop-prod` bucket; orgs live in `/var/lib/context-drop/orgs.json` on the VM's disk.

Deploy the current checkout with `deploy/vm/deploy.sh`. It builds the server, copies it with the settings in `deploy/vm/env` and the admin token from Secret Manager (`context-drop-admin-token`), and restarts it. When `context-drop-slack-client-id` exists in Secret Manager, it also loads `context-drop-slack-client-secret`, `context-drop-slack-signing-secret`, and `context-drop-slack-key`, which turns on [Slack](slack.md) with the shared production app and [Linear](linear.md). Restarting drops every daemon's connection for a second; they reconnect on their own. Logs: `gcloud compute ssh context-drop --zone us-central1-a --project context-drop --tunnel-through-iap -- sudo journalctl -u context-drop -f`. SSH is open only to Google's IAP range (`35.235.240.0/20`), so it always goes through `--tunnel-through-iap`.

The `dari` org's join token is in Secret Manager as `context-drop-join-dari`.
