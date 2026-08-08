# Security and non-goals

- Handoff content is untrusted input and may contain prompt injection, secrets, or malicious files.
- `inspect` never executes content. `accept` stages files only.
- Artifact URLs are bearer links until expiry; targeted delivery does not make a URL private.
- Pairing grants chain access. It does not grant remote execution.
- The hosted relay never starts local agents, opens shells, or reads repositories.
- The local runtime is loopback-only and token-authenticated.
- Inbound handoffs are only notified and never automatically opened/downloaded/accepted/launched.
- Only schedules explicitly created with `context-drop schedule add` can initiate recurring local launches; handoff data is never converted into a schedule or prompt.
- iMessage/SMS is a separate, explicit local execution-request capability for one configured chat. Incoming text is untrusted; the safe Pi responder uses `--print --no-session --no-context-files --no-tools --no-extensions` and receives the text through a private prompt file, not shell interpolation.
- iMessage message IDs are scoped to the configured chat, initial history is marked seen without replies, and IDs are durably claimed before responding to prevent duplicate sends after restart. A crash after a claim may lose one reply rather than replay it.
- Trusted persistent Pi mode is opt-in. Its warm RPC child has the same local permissions and tools as the configured Pi command. Context filtering changes only the provider's working view; it does not delete, truncate, or rewrite the append-only session or configured memory/archive files.
- Daemon, runtime, schedule, prompt, and token files are stored under the private Context Drop state root with user-only permissions.
- Local agents run with the permissions of the local user.

The safe MVP does not implement remote ask, remote launch, automatic dispatch, hosted execution, or arbitrary shell commands. Any future remote-work feature requires explicit capability grants, revocation, replay protection, durable delivery semantics, auditing, and local approval.
