# Examples

## First machine setup

```sh
context-drop init --machine-name laptop
```

## Add a second machine

On the first machine:

```sh
context-drop token create --ttl 5m
```

On the second machine:

```sh
context-drop join <join-token> --machine-name desktop
```

## Share a log for 15 minutes

```sh
context-drop --ttl 15m ./server.log
```

## Upload a screenshot and copy the URL

```sh
context-drop --clipboard ./screenshot.png
```

## Upload the current clipboard image

```sh
context-drop --clipboard
```

## Pull the latest drop

```sh
context-drop pull
```

## Pull a specific drop

```sh
context-drop pull <id> --output ./downloaded.png --force
```

## Wait for the next image from another machine

On the receiving machine:

```sh
context-drop pull --watch --timeout 2m --output ./incoming.png --force
```

On the sending machine, upload an image after watch mode starts:

```sh
context-drop ./capture.png
```

## Send text to one machine

```sh
context-drop machines list
context-drop send --to desktop "look at the latest screenshot"
```

On the target machine:

```sh
context-drop messages list
```

## Send a file to one machine

```sh
context-drop send --to desktop ./spec.md
```

The target sees a message containing the drop id and URL:

```sh
context-drop messages list
context-drop pull <drop-id>
```

## Existing URL passthrough

If the CLI is not initialized or joined, passing an existing URL prints it as-is:

```sh
context-drop https://example.com/already-uploaded.png
```

For machine-readable output:

```sh
context-drop --json https://example.com/already-uploaded.png
```

## Self-host locally

Start the server:

```sh
CONTEXT_DROP_STORAGE=local \
CONTEXT_DROP_DATA_DIR=.data \
CONTEXT_DROP_BASE_URL=http://localhost:8080 \
CONTEXT_DROP_ADDR=:8080 \
go run ./cmd/context-drop-server
```

Point the CLI at it:

```sh
context-drop config set endpoint http://localhost:8080
context-drop init --machine-name laptop
context-drop ./file.txt
```

## Self-host with GCS

```sh
CONTEXT_DROP_STORAGE=gcs \
CONTEXT_DROP_GCS_BUCKET=your-bucket \
CONTEXT_DROP_GCS_PREFIX=context-drop \
CONTEXT_DROP_BASE_URL=https://drop.example.com \
CONTEXT_DROP_ADDR=:8080 \
go run ./cmd/context-drop-server
```

Then initialize the CLI against that endpoint:

```sh
context-drop init --endpoint https://drop.example.com --machine-name laptop
```

## Script with an environment token

```sh
CONTEXT_DROP_ENDPOINT=https://contextdrop.dev \
CONTEXT_DROP_CHAIN_SESSION_TOKEN=<chain-session-token> \
context-drop --json ./artifact.png
```
