# Local visible launch

Initialization detects supported local CLIs and writes a private runtime configuration. The normal always-on path is the Context Drop daemon:

```sh
context-drop init --machine-name laptop
context-drop daemon install
context-drop daemon watchdog install # macOS only
context-drop daemon status
context-drop agent list
context-drop launch --agent pi --repo "$HOME/code/project" --prompt "Inspect tests" --name inspect-tests
context-drop launch --backend herdr --agent pi --repo "$HOME/code/project" --prompt "Inspect tests" --name inspect-tests
# Reuse an existing workspace by creating a new tab inside it:
context-drop launch --backend herdr --workspace w1 --agent pi --repo "$HOME/code/project" --prompt "Inspect tests" --name inspect-tests
context-drop run list
context-drop run show RUN_ID
```

`daemon install` uses a per-user launchd LaunchAgent on macOS and a systemd user service on Linux. The macOS watchdog checks every 15 minutes and reloads an installed daemon service if it is unavailable. Linux relies on `Restart=on-failure`, so a separate watchdog is unnecessary. `daemon start` and `daemon run` are available for unmanaged background and foreground use; `runtime serve` is mainly a debugging escape hatch.

## iMessage request/reply

The same daemon can poll one explicitly configured Messages chat:

```sh
imsg chats --json
context-drop imessage setup --chat-id CHAT_ID --agent pi
context-drop daemon restart
context-drop imessage status --json
```

History and send calls use absolute executable paths and argv arrays. One message is processed at a time. Context Drop claims each message ID durably before invoking the responder; this prevents duplicate sends after restart (a crash after a claim may lose that one reply). Initial sync never replies to history. This adapter produces direct bounded text responses—it does not merely launch a tmux window.

## Durable schedules

```sh
context-drop schedule add --name test-watch --agent pi \
  --backend herdr --repo "$HOME/code/project" --prompt "Inspect current test failures" \
  --every 30m --notify
context-drop schedule list
context-drop schedule run test-watch
context-drop schedule remove test-watch
```

Schedules use fixed intervals with a minimum of one minute. Their agent, absolute repository path, and prompt snapshot are stored privately. `--notify` sends a local notification when a scheduled run starts; launch failures notify by default. Jobs and recent outcomes appear in `schedule list --json` and daemon status.

The runtime binds to loopback and requires its private token. Agent commands and session-manager invocations preserve argv boundaries rather than interpolating prompts into shell commands. Agents run with the local user's permissions in either visible tmux windows or Herdr. By default, a lane-less Herdr run creates a tab in the reusable `ContextDropManaged` workspace in session `default`; `--workspace wN` instead makes the run human-copilot work and creates a new tab in that selected existing workspace without disturbing its current panes. `--backend tmux|herdr` overrides the configured default for one launch or schedule; new installations default to Herdr, while an existing runtime config keeps its explicitly stored backend. Human-copilot Herdr launches use the configured named session (default: `default`). Missing optional CLIs do not prevent pairing or handoffs.

Inbox polling is notification-only: inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.
