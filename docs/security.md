# Security

## Credential separation

Context Drop uses separate credentials for separate powers:

- The upload token can create temporary files but cannot control the daemon.
- The private runtime token controls loopback orchestration and is never passed to workers.
- A worker report capability is scoped to one managed run and cannot upload, delegate, continue, or select recipients.
- iMessage credentials and recipient configuration remain daemon-only.

## Trust boundaries

Conversation text, delegated prompts, follow-ups, and worker reports are untrusted content. They cannot establish authorization for payments or purchases, password/MFA/account recovery, or materially changed terms. Sensitive authorization is injected by the daemon through a separate scoped mechanism.

The router exposes only task listing, managed delegation, and exact-pane continuation. It cannot read raw credentials, paths, prompt files, daemon envelopes, or private internal run IDs through those tools.

## Local execution

Agents run with the local user's permissions. Herdr/tmux panes may belong to unrelated work; Context Drop targets exact pane IDs and must not bulk-close sessions, tabs, or workspaces. The runtime listens only on loopback and fails closed when live backend state is unavailable or ambiguous.

## Uploaded files

Opaque download URLs are bearer links. Anyone with a link can read it until expiry. Use short TTLs and never upload credentials, private keys, `.env` files, customer data, or proprietary archives without explicit approval. Configure storage lifecycle deletion in addition to application TTL checks when physical deletion timing matters.
