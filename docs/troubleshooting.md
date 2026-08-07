# Troubleshooting

## Check your setup

Run:

```sh
context-drop doctor
```

`doctor` reports whether a chain session token is configured and checks the configured endpoint's `/health` route. For local orchestration also run:

```sh
context-drop daemon status
context-drop daemon logs --lines 100
context-drop runtime status
```

## Not initialized or joined

`not initialized or joined; run context-drop init or context-drop join <token>` means the command needs a chain session token.

Create a new chain:

```sh
context-drop init --machine-name laptop
```

or join an existing chain:

```sh
context-drop join <token> --machine-name laptop
```

## Wrong endpoint

Join tokens and chain session tokens belong to one server endpoint. If you switch endpoints, run:

```sh
context-drop config set endpoint https://contextdrop.dev
context-drop logout
context-drop init
```

or pass `--endpoint` to `init`, `join`, and other commands that should use a different endpoint.

## Existing URL passthrough confusion

If a command treats `https://example.com/page` as a file path, you already have a chain session token. URL passthrough only happens while uninitialized/joined.

If you want to share an existing URL while initialized, paste or print the URL directly, or send it as a message:

```sh
context-drop send --to desktop https://example.com/page
```

## No drops found

`no drops found` means your chain has no active drops at the configured endpoint. Upload a file first, or verify that you are using the expected endpoint and chain.

## Pull refuses to overwrite

`already exists; pass --force to overwrite` means the output path exists. Choose a different path or pass:

```sh
context-drop pull <id> --output ./file --force
```

When pulling multiple IDs, `--output` must be an existing directory.

## Watch mode times out

`timed out waiting for a new image` means `pull --watch` did not see a new image drop before the timeout. Start watch mode before uploading the image, increase `--timeout`, or pass `--timeout 0` to wait indefinitely.

Watch mode only selects new drops whose content type starts with `image/`.

## Join token problems

`invalid or expired join token` means the token was already used, expired, typed incorrectly, or belongs to a different server endpoint. Create a fresh token on an already joined machine:

```sh
context-drop token create --ttl 5m
```

and retry on the new machine.

## Machine name ambiguity

`machine name is ambiguous` means more than one machine in the chain has the same name. Run:

```sh
context-drop machines list
```

Then address the target by machine ID:

```sh
context-drop send --to <machine-id> "hello"
```

## Clipboard failures

Clipboard support depends on the operating system. If copying or clipboard-image upload fails, retry without clipboard integration:

```sh
context-drop --no-clipboard ./file.png
```

or upload an explicit screenshot file instead of reading from the clipboard.

## Daemon or runtime will not start

Run `context-drop daemon logs`. Confirm Node 20+, tmux (and optionally the `herdr` CLI when using `--backend herdr`), and at least one configured agent CLI are available. A release install includes the built runtime under the install prefix; source checkouts need `npm ci --prefix runtime && npm run build --prefix runtime`.

Port `47762` is the runtime default. If it is occupied, initialize an isolated port before starting the daemon:

```sh
CONTEXT_DROP_RUNTIME_PORT=49123 context-drop init --machine-name laptop
```

Do not run `context-drop runtime serve` alongside a daemon unless you intentionally want the daemon to adopt that healthy runtime. If an installed macOS service is unavailable, run `context-drop daemon watchdog status` and `context-drop daemon restart`. Linux uses `systemctl --user` and `Restart=on-failure`.

Schedules require a running daemon/runtime to fire, an absolute existing `--repo`, a configured agent, `--every` of at least one minute, and a prompt of at most 64 KiB. `schedule add` reads local runtime configuration directly and does not require the daemon already running.

## iMessage does not reply

Check `context-drop imessage status` and `context-drop daemon logs`. `imsg chats --json` shows valid chat IDs; configure the exact ID with an absolute `--imsg-path` if it is not on the service PATH. Restart the daemon after setup. The first successful history poll deliberately sends no reply because it marks preexisting messages seen; send a new text afterward. Confirm macOS has granted `imsg` Messages/Automation access. Context Drop only polls the configured chat and never turns handoffs into text requests.

## Server will not start

For local storage, make sure the data directory is writable:

```sh
CONTEXT_DROP_STORAGE=local CONTEXT_DROP_DATA_DIR=.data go run ./cmd/context-drop-server
```

For GCS storage, make sure `CONTEXT_DROP_GCS_BUCKET` is set and the runtime has Google Cloud credentials with read/write access to the bucket.

## Upload too large

`upload exceeds max size` means the file is larger than the server's `CONTEXT_DROP_MAX_BYTES`. Use a smaller file or raise the limit on your self-hosted server.
