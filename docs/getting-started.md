# Getting started

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/mupt-ai/context-drop/main/install.sh | bash
context-drop version
```

## Initialize your first machine

Create a new machine chain and register this machine:

```sh
context-drop init --machine-name laptop
```

If you omit `--machine-name`, Context Drop uses your hostname when available and falls back to `machine`. Initialization also creates the private runtime configuration and detects installed `pi`, `codex`, and `claude` CLIs.

Install the always-on per-user daemon:

```sh
context-drop daemon install
context-drop daemon watchdog install # macOS only; Linux uses systemd restart
context-drop daemon status
```

For a temporary foreground session use `context-drop daemon run`; `context-drop daemon start` starts an unmanaged background process when no service is installed.

## Optional: answer one iMessage/SMS chat

Install `imsg`, grant its required macOS permissions, and find the chat ID:

```sh
imsg chats --json
```

Then configure Context Drop without sending anything and restart the daemon:

```sh
context-drop imessage setup --chat-id CHAT_ID --recipient PHONE_OR_EMAIL --agent pi
context-drop daemon restart
context-drop imessage status
```

The initial history sync marks existing incoming texts seen. Send a new text only after status reports `Initialized: true`. The default responder is local Pi in print mode with tools/context/extensions/session persistence disabled.

## Upload and pull

Upload a file and print a temporary URL:

```sh
printf 'hello from Context Drop\n' > /tmp/context-drop-demo.txt
context-drop --ttl 1h /tmp/context-drop-demo.txt
```

List active drops in your chain:

```sh
context-drop list
```

Pull the latest active drop:

```sh
context-drop pull
```

Pull a specific drop to a chosen path:

```sh
context-drop pull <id> --output ./downloaded.txt --force
```

## Add another machine

On an already initialized or joined machine, create a short-lived join token:

```sh
context-drop token create --ttl 5m
```

On the new machine, join with that token:

```sh
context-drop join <join-token> --machine-name desktop
```

Any joined machine can create the next join token.

## Send to one machine or agent

List machine names and ids:

```sh
context-drop machines list
```

Send text to one machine:

```sh
context-drop send --to desktop "hello from laptop"
```

Send a file to one machine. If the argument is a single existing file path, Context Drop uploads it and sends a message containing the drop id and URL:

```sh
context-drop send --to desktop ./spec.md
```

On the target machine:

```sh
context-drop messages list
context-drop pull <drop-id>
```

## Clipboard

Copy an uploaded URL to your clipboard:

```sh
context-drop --clipboard ./screenshot.png
```

Upload the current clipboard image:

```sh
context-drop --clipboard
```

Clipboard integration is disabled by default. To enable URL copying by default:

```sh
context-drop config set clipboard true
```

Use `--no-clipboard` for a one-command override.

## Existing URL passthrough

If the CLI is not initialized/joined and you pass an existing `http` or `https` URL, Context Drop prints that URL as-is and warns that initialization is required for uploads:

```sh
context-drop https://example.com/already-uploaded.png
```

This is only an uninitialized fallback. Once you have a chain session token, `context-drop <argument>` treats the argument as a local file path to upload.

## Schedule explicit local work

```sh
context-drop schedule add --name hourly-check --agent pi \
  --repo "$HOME/code/project" --prompt "Inspect the current test failures" \
  --every 1h --notify
context-drop schedule list
```

The daemon launches this snapshotted prompt locally in tmux at the fixed interval. Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.
