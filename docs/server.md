# Server and self-hosting

The hosted Context Drop service is a small TTL file store. It exposes health checks, one authenticated upload endpoint, and opaque public download URLs. It has no chains, machine pairing, inboxes, handoffs, listing, pull, or delete APIs.

## API

- `GET /health` and `GET /healthz` return `ok`.
- `POST /v1/drops` uploads a non-empty body. It requires `Authorization: Bearer <CONTEXT_DROP_UPLOAD_TOKEN>` and accepts `X-Filename`, `Content-Type`, and `X-TTL` headers.
- `GET /d/<opaque-id>` downloads a file until its TTL expires. Anyone with the unguessable URL can open it; expired links return `410 Gone`.

The server retains content-type and size limits. Safe image, PDF, and text types may render inline; other types download as attachments. There is no listing endpoint. Storage objects may remain physically present after expiry, so configure backend lifecycle rules when deletion timing matters.

## Run locally

```sh
CONTEXT_DROP_UPLOAD_TOKEN='replace-with-a-long-random-secret' \
CONTEXT_DROP_STORAGE=local \
CONTEXT_DROP_DATA_DIR=.data \
CONTEXT_DROP_BASE_URL=http://localhost:8080 \
CONTEXT_DROP_ADDR=:8080 \
go run ./cmd/context-drop-server
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
go run ./cmd/context-drop-server
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
| `CONTEXT_DROP_UPLOAD_TOKEN` | yes | — | Bearer token accepted by `POST /v1/drops`. |
| `CONTEXT_DROP_ADDR` | no | `:8080` | Listen address. |
| `CONTEXT_DROP_BASE_URL` | no | `http://localhost:8080` | Public origin used in returned download URLs. |
| `CONTEXT_DROP_STORAGE` | no | `local` | `local` or `gcs`. |
| `CONTEXT_DROP_DATA_DIR` | no | `.data` | Local storage root. |
| `CONTEXT_DROP_GCS_BUCKET` | for GCS | — | GCS bucket name. |
| `CONTEXT_DROP_GCS_PREFIX` | no | — | Optional GCS object prefix. |
| `CONTEXT_DROP_DEFAULT_TTL` | no | `24h` | Default link lifetime. |
| `CONTEXT_DROP_MAX_TTL` | no | `168h` | Maximum accepted `X-TTL`. |
| `CONTEXT_DROP_MAX_BYTES` | no | `26214400` | Maximum upload size. |
