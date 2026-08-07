# Architecture and trust boundary

Context Drop has two local processes and an optional hosted relay:

- **Go CLI/server/daemon:** pairing, machine-chain auth, artifact storage, handoff manifests, inbox APIs, safe staging, durable local schedules, OS notifications, and background-service supervision.
- **Node runtime:** loopback-only authenticated API for configured local agents, visible tmux or Herdr launches, and local run records. The Go daemon normally supervises it.
- **Relay:** routes authenticated chain requests and stores short-lived artifacts. It does not run agents or access local repositories.

A handoff crosses machines as data. The recipient must inspect and accept it. Only a separate local `context-drop launch`, `context-drop schedule run`, or previously configured explicit schedule can start an agent. Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.

The runtime uses argv arrays and writes prompts to private files; it never builds shell command strings from handoff content.
