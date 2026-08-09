# Architecture and trust boundary

Context Drop has two local processes and an optional hosted relay:

- **Go CLI/server/daemon:** pairing, machine-chain auth, artifact storage, handoff manifests, inbox APIs, safe staging, durable local schedules, OS notifications, and background-service supervision.
- **Node runtime:** loopback-only authenticated API for configured local agents, visible tmux or Herdr launches, local run records, and router/task-worker delegation. The Go daemon normally supervises it; per-task report capabilities are separate from the general runtime token.
- **Relay:** routes authenticated chain requests and stores short-lived artifacts. It does not run agents or access local repositories.

A handoff crosses machines as data. The recipient must inspect and accept it. Only a separate local `context-drop launch`, `context-drop schedule run`, or previously configured explicit schedule can start an agent. Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.

The runtime uses argv arrays and writes prompts to private files; it never builds shell command strings from handoff content. The iMessage router receives only `delegate(task)` through a rotating capability bound server-side to its exact chat. Workers receive a per-run extension-scoped `report_to_parent` capability and reports preserve immutable router/chat ownership through lease/ack delivery. Sensitive action reports require a daemon-issued exact-chat confirmation challenge; TASK prose cannot mint authorization. Follow-ups always reach the router, which may choose a new-worker fallback because active-run steering is not supported by the runtime API. Runtime state has a single-writer lock to prevent overlapping runtime processes from racing durable report/task updates.
