# Architecture and trust boundary

Context Drop has two local processes and an optional hosted relay:

- **Go CLI/server/daemon:** pairing, machine-chain auth, artifact storage, handoff manifests, inbox APIs, safe staging, durable local schedules, OS notifications, and background-service supervision.
- **Node runtime:** loopback-only authenticated API for configured local agents, visible tmux or Herdr launches, local run records, and router/task-worker delegation. The Go daemon normally supervises it; per-task report capabilities are separate from the general runtime token.
- **Relay:** routes authenticated chain requests and stores short-lived artifacts. It does not run agents or access local repositories.

A handoff crosses machines as data. The recipient must inspect and accept it. Only a separate local `context-drop launch`, `context-drop schedule run`, or previously configured explicit schedule can start an agent. Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.

The runtime uses argv arrays and writes prompts to private files; it never builds shell command strings from handoff content. The iMessage router receives only `delegate(task)`, workers receive an extension-scoped `report_to_parent` capability, and follow-ups use a new-worker fallback because active-run steering is not supported by the runtime API.
