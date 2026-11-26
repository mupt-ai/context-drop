# Linear

An org can connect one Linear workspace. Then agents comment on issues, and people's later comments on those issues come back to the agent.

```sh
context-drop linear install                          # once per org; paste a workspace admin's API key
context-drop send linear:ENG-123 'Fixed in #412. OK to close?'
context-drop linear read ENG-123                     # the issue's latest comments
```

- **Setup:** in Linear, a workspace admin creates a personal API key (Settings → Security & access → Personal API keys). `linear install` checks it, creates a webhook for comments in every public team, and seals the key on the server. There is no app to create. Agents' comments appear under that key's user.
- **Replies:** a new comment on an issue an agent commented on is pushed to every agent that commented there, as its next prompt. Comments on other issues are never pushed.
- **Who can reach agents:** anyone who can comment on the issue in your Linear workspace.
- **Mods:** with `"linear"` in `can`, `cd.send("linear:ENG-123", text)` comments and `cd.linear.read("linear:ENG-123")` reads.
- **Off:** `context-drop linear uninstall` deletes the webhook and forgets the key. Revoke the key in Linear too.

Routes from issues to agents last 30 days after their last use. `send --wait` is Slack-only; on Linear the reply arrives as the agent's next prompt.
