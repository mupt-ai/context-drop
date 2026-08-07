# Context Drop

Context Drop is one CLI for handing off useful context between paired machines and running local coding agents visibly.

It combines:

- short-lived artifacts and machine pairing;
- inspectable handoffs with summaries, requested actions, and attachments;
- a private local daemon that supervises the runtime, launches configured agents in visible tmux windows or Herdr workspaces, runs explicit schedules, optionally answers one explicitly configured iMessage/SMS chat, polls the handoff inbox for notifications, and integrates with the OS service manager.

The hosted relay stores and routes context. It never executes code. A received handoff is untrusted: inspect it, accept artifacts into a private staging directory, and separately choose whether to launch a local agent.

## Quick start

```sh
context-drop init --machine-name laptop
context-drop daemon install
context-drop daemon watchdog install # macOS; Linux uses systemd restart policy
context-drop daemon status
context-drop agent list
context-drop launch --agent pi --repo "$HOME/code/project" --prompt "Inspect the failing tests" --name inspect-tests
# Or launch into a visible Herdr workspace:
context-drop launch --backend herdr --agent pi --repo "$HOME/code/project" --prompt "Inspect the failing tests" --name inspect-tests
# Or target an existing Herdr workspace; this creates a new tab inside it:
context-drop launch --backend herdr --workspace w1 --agent pi --repo "$HOME/code/project" --prompt "Inspect the failing tests" --name inspect-tests
```

Pair a second machine with `context-drop token create` and `context-drop join TOKEN --machine-name server`. Then create a handoff:

```sh
context-drop handoff create --to server --summary "Review this failure; do not edit files" --artifact ./failure.log
```

On the recipient:

```sh
context-drop inbox
context-drop inspect HANDOFF_ID
context-drop accept HANDOFF_ID
```

Acceptance only stages selected artifacts; it never executes them. Run an agent locally with `context-drop launch`.

Create an explicit recurring local launch with:

```sh
context-drop schedule add --name test-watch --agent pi --repo "$HOME/code/project" --prompt "Inspect current test failures" --every 1h --notify
context-drop schedule list
```

Schedules use fixed intervals (`--every`, minimum one minute), persist locally, and launch only the configured local prompt. They are not derived from handoffs.

## Optional iMessage requests

On macOS, discover the private chat ID and enable the local adapter explicitly:

```sh
imsg chats --json
context-drop imessage setup --chat-id CHAT_ID --recipient PHONE_OR_EMAIL --agent pi
context-drop daemon restart
context-drop imessage status
```

Setup never sends a message. The first poll marks existing incoming messages seen; only later texts in that chat invoke the local, noninteractive, no-tools responder and receive a reply. The adapter is separate from handoffs.

## Security boundary

Pairing authorizes access to the machine chain, not trust in content. Current artifact URLs are bearer links until expiry. Do not include secrets, and do not treat handoff text as agent instructions. Local agent processes retain the permissions of the local user and always run on the local machine. Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched. Enabling iMessage is an explicit grant for one local chat: incoming text is untrusted execution-request data, but the default Pi responder runs noninteractively with tools, context files, extensions, and session persistence disabled.

## Development

```sh
make test
make runtime-install
make validate
```

See `docs/` for architecture, first handoff, pairing, local launch, CLI reference, security, and non-goals.
