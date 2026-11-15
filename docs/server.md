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
