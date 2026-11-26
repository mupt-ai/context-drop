# Linear

An org can connect one Linear workspace. Then agents comment on issues, and people's later comments on those issues come back to the agent.

```sh
context-drop linear install                          # once per org: approve the app in your browser
context-drop send linear:ENG-123 'Fixed in #412. OK to close?'
context-drop linear read ENG-123                     # the issue's latest comments
```

- **Setup:** `linear install` opens Linear, a workspace admin approves Context Drop, and that's it. Agents' comments appear as the app. On a server without a Linear app, `linear install` asks for a workspace admin's personal API key instead and creates the webhook itself; comments then appear under that key's user.
- **Replies:** a new comment on an issue an agent commented on is pushed to every agent that commented there, as its next prompt. Comments on other issues are never pushed.
- **Who can reach agents:** anyone who can comment on the issue in your Linear workspace.
- **Mods:** with `"linear"` in `can`, `cd.send("linear:ENG-123", text)` comments and `cd.linear.read("linear:ENG-123")` reads.
- **Off:** `context-drop linear uninstall` deletes the webhook and forgets the key. Revoke the key in Linear too.

Routes from issues to agents last 30 days after their last use. `send --wait` is Slack-only; on Linear the reply arrives as the agent's next prompt.

## Setting up the app (server operators)

At https://linear.app/settings/api/applications, create an application:

| Field | Value |
| --- | --- |
| Callback URL | `https://<your server>/v1/linear/oauth` |
| Webhooks | on, URL `https://<your server>/v1/linear/events`, events: **Comments** |
| Public | on, so other workspaces can install it |

Then set `CONTEXT_DROP_LINEAR_CLIENT_ID`, `CONTEXT_DROP_LINEAR_CLIENT_SECRET`, and `CONTEXT_DROP_LINEAR_WEBHOOK_SECRET` (the app's webhook signing secret) on the server, with `CONTEXT_DROP_TOKEN_KEY`, and restart it. Access tokens last a day; the server refreshes them.
