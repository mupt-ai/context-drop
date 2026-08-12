# Troubleshooting

## Upload token is required

Set a token accepted by the selected TTL service:

```sh
export CONTEXT_DROP_ENDPOINT=https://contextdrop.dev
export CONTEXT_DROP_UPLOAD_TOKEN='...'
context-drop upload ./file.txt
```

An upload token is not a daemon or worker credential.

## Daemon or runtime is unavailable

```sh
context-drop daemon status
context-drop daemon logs --lines 200
context-drop daemon restart
```

Node.js 20+ and the installed runtime assets are required. A runtime port occupied by a different process is reported rather than silently bypassed.

## No configured agent

Install and authenticate a supported agent CLI (`pi`, `codex`, or `claude`), then restart the daemon so initialization can detect it. Advanced installations may provision the runtime JSON directly with an absolute argv containing exactly one `{prompt_file}` placeholder.

## Herdr status is unavailable

Verify `HERDR_ENV=1`, that the configured `default` session is running, and that the configured Herdr executable is valid. Choose tmux during initial runtime creation with `CONTEXT_DROP_BACKEND=tmux` if Herdr is not available.

## iMessage is not configured

The public CLI does not include adapter setup commands. Verify the private `imessage/config.json`, its executable paths and permissions, macOS Messages access for `imsg`, then restart the daemon. The current release does not implement Telegram.

## Worker reporting is not configured

`context-drop report` is intended for a fully managed worker. The daemon must inject `CONTEXT_DROP_REPORT_URL`, `CONTEXT_DROP_REPORT_CAPABILITY`, and `CONTEXT_DROP_RUN_ID`. Do not manually reuse or share those values.

## Clipboard upload fails

Pass a file path instead, or install `pngpaste` on macOS / `wl-paste` or `xclip` on Linux. Clipboard integration is opt-in unless enabled in the private config file.
