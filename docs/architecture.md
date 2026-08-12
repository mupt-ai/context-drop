# Architecture

Context Drop has three intentionally separate boundaries.

## Messaging daemon

The Go daemon owns transport credentials, durable conversation cursors, schedule state, report delivery, and runtime supervision. The current transport adapter is one explicitly configured private iMessage chat on macOS. Telegram is not yet implemented.

The daemon starts or connects to a loopback-only Node runtime and rotates a capability scoped to the immutable conversation owner. Workers never receive messaging credentials or the runtime's general credential.

## Orchestrator runtime

A persistent orchestrator responds to conversation turns and has exactly three task tools: `list_tasks`, `delegate_task`, and `continue_task`. Live status comes only from the configured Herdr or tmux backend and fails visibly when that backend is unavailable.

Delegated tasks are fully managed: Context Drop injects scoped reporting context, records their pane, and monitors their lifecycle. Public task identity is the backend pane ID, not a private run ID, prompt path, terminal title, or daemon envelope. Continuation targets any exact live pane, including an unmanaged pane.

Worker `context-drop report` messages are natural language. They enter the owning orchestrator as ordinary untrusted messages. Separately marked daemon lifecycle events cover crashes and disappearing managed panes.

## TTL upload service

The optional HTTP server is only a file store:

- authenticated `POST /v1/drops`
- opaque public `GET /d/<id>` until expiry
- health endpoints

Upload credentials are independent from runtime and report capabilities. The service has no accounts, pairing, machine graph, inbox, messages, handoffs, task state, or remote execution.
