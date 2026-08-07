# Server and self-hosting

Context Drop can use the hosted endpoint at `https://contextdrop.dev` or a self-hosted server that you run. Self-hosting is useful when you need your own storage bucket, domain, upload size limits, TTL policy, or private deployment boundary.

The server binary reads runtime configuration from environment variables. Run `context-drop-server --help` for a quick summary.

## What the server provides

The server exposes health checks, chain initialization, join-token pairing APIs, authenticated upload/list/pull APIs, public drop URLs, machine listing, and chain messages. The CLI is the intended user interface for most workflows.

Public drop URLs are served under:

```text
/d/<drop-id>
```

Authenticated CLI APIs are served under `/v1`. Upload/list/pull/delete require a chain session token. Public drop URLs can be fetched by anyone who has the URL until expiry.

## Prerequisites

A self-hosted deployment needs a storage backend. Use `local` for local development or small single-node deployments. Use `gcs` for production deployments where server instances should share storage or survive container restarts.

For production, put the server behind HTTPS and set `CONTEXT_DROP_BASE_URL` to the public HTTPS origin that users will open.

## Run locally with filesystem storage

This example starts the server on `http://localhost:8080` and stores data under `.data`:

```sh
CONTEXT_DROP_STORAGE=local \
CONTEXT_DROP_DATA_DIR=.data \
CONTEXT_DROP_BASE_URL=http://localhost:8080 \
CONTEXT_DROP_ADDR=:8080 \
go run ./cmd/context-drop-server
```

In another shell, point the CLI at the local server and initialize a chain:

```sh
context-drop config set endpoint http://localhost:8080
context-drop init --machine-name laptop

printf 'hello self-hosted Context Drop\n' > /tmp/context-drop-local.txt
context-drop /tmp/context-drop-local.txt
context-drop list
context-drop pull
```

If you do not want to change your default endpoint, pass `--endpoint http://localhost:8080` on each command instead.

## Run with Google Cloud Storage

For a shared or production deployment, configure the GCS backend:

```sh
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

The server uses normal Google Cloud authentication. For local testing, set `GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json`. On Cloud Run or another Google-managed runtime, prefer a runtime service account with permission to read and write objects in the bucket.

## Run the Docker image you build from this repo

The included `Dockerfile` builds `context-drop-server` into a small Alpine image. Build and run it locally with an env file:

```sh
docker build -t context-drop-server .

cat > .env.local <<'EOF'
CONTEXT_DROP_STORAGE=local
CONTEXT_DROP_DATA_DIR=/data
CONTEXT_DROP_BASE_URL=http://localhost:8080
CONTEXT_DROP_ADDR=:8080
EOF

docker run --rm -p 8080:8080 --env-file .env.local -v "$PWD/.data:/data" context-drop-server
```

For GCS, add the GCS variables and provide credentials through your platform's normal secret or identity mechanism.

## Build and run from a checkout

The Makefile contains the common source-checkout commands:

```sh
make test
make build
make install
make run
make run-server
```

`make install` installs the CLI binary. `make run-server` runs `go run ./cmd/context-drop-server`, so it needs the same server environment variables shown above.

## Storage behavior

With `CONTEXT_DROP_STORAGE=local`, the server stores drops and pairing state under `CONTEXT_DROP_DATA_DIR`. This is simple and transparent, but every server instance needs the same local disk to see the same data.

With `CONTEXT_DROP_STORAGE=gcs`, the server stores drops and pairing state in `CONTEXT_DROP_GCS_BUCKET`, optionally under `CONTEXT_DROP_GCS_PREFIX`. This is the recommended backend for multi-instance or containerized deployments.

Expired drops are no longer listed and public drop URLs return an expiry error after the TTL. The server does not promise physical object deletion at the exact expiry instant, so use storage lifecycle rules if you need automatic cleanup of expired data.

## Health checks

The server responds with `ok` on both health endpoints:

```sh
curl http://localhost:8080/health
curl http://localhost:8080/healthz
```

The CLI's `context-drop doctor` command checks the configured endpoint's `/health` route.

## Server environment reference

The full environment variable table lives in [Configuration](configuration.md#server-environment-variables). The minimum local configuration is no configuration at all; defaults listen on `:8080`, store data under `.data`, and generate links from `http://localhost:8080`.

## Hosted endpoint usage

To return to the hosted endpoint after testing a local server, set:

```sh
context-drop config set endpoint https://contextdrop.dev
```

Then run:

```sh
context-drop doctor
```

If your saved chain session belongs to a different endpoint, run `context-drop logout` and initialize or join a chain against the intended endpoint.
