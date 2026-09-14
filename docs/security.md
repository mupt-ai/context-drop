# Security

The upload token, runtime token, main delegation capability, native worker event capabilities, and task report capabilities have separate scopes. Workers receive no daemon messaging credentials or general runtime token. Task capabilities stop working after completion and remain stable while the same task receives follow-ups.

The main agent has one delegation tool and uses its final text for messaging. Worker-report turns expose no delegation tools. Worker output is attributed data, not authorization for new work. The former confirmation-token and automatic-authorization execution paths are removed.

Workers use the configured agent's normal coding tools, with its approval prompts bypassed, in native Herdr tabs and execute authorized tasks directly, including API writes. Orchestrator-only delegation instructions do not restrict worker execution. Coding work may use Herdr agents when requested or required by repository instructions; workers do not manage the Context Drop pool. Results return through the report pipeline. These are local agents running as the user, not OS-sandboxed processes; capability separation restricts Context Drop's HTTP APIs, not arbitrary access to the user's filesystem. Current worker instructions are applied on both fork and continuation.

The runtime listens only on loopback. Native operations target exact persisted pane IDs created for the pool. Ambiguous launches and submissions are never automatically replayed. Pending reports use durable outboxes and leased delivery; ambiguous external message delivery is parked rather than retried.

Upload URLs are bearer links until expiry. Keep credentials and sensitive files out of uploads, and use short TTLs when appropriate.
