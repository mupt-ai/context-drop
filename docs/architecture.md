# Architecture

Context Drop has two agent levels: one main conversation and exactly four Codex workers.

The Go daemon owns iMessage, schedule timing, direct script execution, delivery receipts, and runtime supervision. Command schedules bypass the agent pool. The main Pi conversation can text the user through its final response and call `delegate_to_worker(worker, prompt)`. No shell, pane inspection, repository launch, thread management, or automatic authorization tools are exposed to it. Report turns cannot delegate work.

The Node runtime owns a durable FIFO task queue and four fixed slots. Each slot is a visible Codex TUI in a Herdr tab or tmux pane, connected to a private capability-authenticated Codex app server. Workers start without a model request. The runtime imports the effective main conversation and its saved compaction summary into a Codex parent thread, forks it natively, and switches the existing TUI with `/resume`. There is no extra summarization call or process startup per task. Model/network calls and local dispatch still take time.

Messages and schedule occurrences enter the same queue. Explicit worker selection is respected; schedules use the first available slot. A waiting worker retains its task and session. Delegating the user's answer to that worker resumes the same fork. A busy worker queues new tasks rather than silently treating unrelated messages as follow-ups.

Codex turn completion reports final answers automatically. Explicit `context-drop report` calls send progress; `--question` asks for user input and leaves the worker waiting when its turn ends. All reports reach the main conversation with worker and task attribution. Workers do not have delegation or messaging tools.

Queue state and task-scoped report capabilities are persisted before native dispatch. Each schedule occurrence and main tool call has an idempotency key. Resumed turns have fresh identities within the same task capability. Codex turn history allows completion recovery after runtime outages. Report delivery uses leases and receipts; ambiguous external sends are not retried automatically.

Native workers survive runtime restarts. Running work is not resubmitted. A dispatch whose outcome is unknown retains its slot instead of risking duplicate execution. Pool status is available through `context-drop daemon status`.

The optional TTL upload server is independent: authenticated uploads and expiring bearer download links. It owns no agent or schedule state.

Incoming texts are grouped after a two-second quiet period (six-second maximum), retaining each original message as a durable job. One combined turn can finish silently. Delegation to an occupied worker adds context to the same task; pending additions run in the same session before its final report is delivered. Explicit `newTask` queues unrelated work separately. Parent instructions, including loaded AGENTS.md files, accompany each fork and are combined with the worker role. Worker questions and results are relayed naturally, without worker-number wrappers.

The Codex command accepts a launcher prefix such as `["dari", "--codex", "--yolo"]`. The app server runs through that launcher, including its provider configuration. Remote TUIs use the same launcher but omit permission flags, since permission policy is set on the server and inherited by every worker thread.
