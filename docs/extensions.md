# Extensions

Extensions connect an org to where its people already talk. Each one owns a kind of `send` target, posts there for agents, and pushes people's replies back to the agent that is waiting on them.

| Extension | Targets | A reply that reaches the agent | Guide |
| --- | --- | --- | --- |
| Slack | `#channel`, `@person`, `slack:CHANNEL/TS` | A reply in the agent's thread, or a DM to the app | [slack.md](slack.md) |
| Linear | `linear:ENG-123` | A new comment on an issue the agent commented on | [linear.md](linear.md) |

Everything else stays where it is, and agents read it on demand (`slack read`, `linear read`). A mod uses an extension when its name is in the mod's `can` list.

The server turns extensions on with `CONTEXT_DROP_TOKEN_KEY`, which seals the tokens they store. Each lives in its own package under `cli/internal/extensions/`, behind the `Extension` interface in `extensions.go`: a name, the targets it owns, its routes, and `Send`.
