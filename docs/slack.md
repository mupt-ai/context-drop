# Slack

An org can connect one Slack workspace. Then `send` reaches people as well as agents, and people answer agents from Slack.

```sh
context-drop send '#deploys' 'v2 is ready. Ship it to prod?' --wait
context-drop send @avyay 'Which region should the new bucket go in?'
context-drop send slack:C0123ABCD/1700000000.000100 'Done, it is live.'
```

## How messages flow

| Message | Reaches an agent? |
| --- | --- |
| A reply in a thread an agent started or posted in | Pushed to every agent in that thread, as its next prompt |
| A DM to the app | Pushed to the agent that last messaged that person |
| Anything else in a channel | Never pushed. Agents pull it with `slack read` (or the `slack_read` tool) |

Only people linked to the org reach agents. The easy way is for each person to run `context-drop slack connect` on their own machine: it opens Slack, they sign in, and their account is linked. `context-drop slack allow @person` links someone else directly. Anyone else who DMs the app is told they are not linked, and their messages go nowhere. Each pushed message names the person and ends with the `send` target to reply on, and the app reacts with 👀 once it is delivered. If the agent is offline, the person is told in the thread.

An agent decides on its own when it needs a person. The `send` tool's description (through `context-drop mcp`) tells it when: a decision or approval only a person can give, access or information no agent has, being stuck after trying, or reporting back on something it was asked. It does not need to poll: the reply arrives as its next prompt.

## Targets

- `#channel`: a public or private channel the app can see. The app joins public channels on its first post.
- `@person`: a DM with the app. The person is a Slack handle, display name, email, or user ID.
- `slack:CHANNEL/TS`: a thread. `send` prints this for every message it posts, and pushed messages include it.

Agent-to-person messages carry anything; there is no filter on what an agent may post. `send --file` is not supported for Slack; use `context-drop upload` and send the link.

## Commands

```sh
context-drop slack install            # once per org: connect a workspace (opens your browser)
context-drop slack connect            # link your own Slack account (opens Slack; sign in)
context-drop slack status             # the workspace and linked people
context-drop slack allow @avyay @sam  # let them answer agents
context-drop slack disallow @sam
context-drop slack read '#deploys' -n 50
context-drop slack read slack:C0123ABCD/1700000000.000100
context-drop slack uninstall
context-drop slack manifest --server https://ctx.example.com
```

`send --wait` to a Slack target prints the first replies from people. Agents do not need it; their replies are pushed.

## Mods

A mod with `"slack"` in its `can` list can use the workspace with the org's bot token:

```ts
export default mod({
  name: "standup",
  description: "Post every agent's status to #standup",
  can: ["list", "slack"],
  async run(_, cd) {
    const agents = await cd.list()
    const lines = agents.map((a) => `• \`${a.id}\` ${a.status} in ${a.cwd}`)
    await cd.send("#standup", lines.join("\n"))
  },
})
```

- `cd.send("#channel" | "@person" | "slack:C/TS", text)` posts as the mod.
- `cd.slack.read(target, { limit, oldest })` reads messages, oldest first.
- `cd.slack.api(method, args)` calls a Web API method: `chat.*`, `conversations.*`, `reactions.*`, `pins.*`, `bookmarks.*`, `users.info`, `users.list`, `users.lookupByEmail`, `users.profile.get`, `team.info`, `files.info`, and `files.list`. Methods that administer the workspace or the app, and `chat.delete`, `conversations.kick`, `conversations.archive`, and `conversations.rename`, are refused.

Mods cannot link or unlink people; only machines can change who reaches agents.

## Setting up the app

The server operator creates the Slack app once; each org then installs it into its own workspace.

1. Print the manifest for your server: `context-drop slack manifest --server https://ctx.example.com > manifest.yaml`.
2. At https://api.slack.com/apps, choose **Create New App → From a manifest**, pick a workspace, and paste it. To let other workspaces install it, turn on **Manage Distribution**.
3. From **Basic Information**, copy the client ID, client secret, and signing secret into the server's environment, with a token key:

   | Variable | Value |
   | --- | --- |
   | `CONTEXT_DROP_SLACK_CLIENT_ID` | the app's client ID |
   | `CONTEXT_DROP_SLACK_CLIENT_SECRET` | the app's client secret |
   | `CONTEXT_DROP_SLACK_SIGNING_SECRET` | the app's signing secret |
   | `CONTEXT_DROP_TOKEN_KEY` | a long random secret that seals each org's bot token at rest; changing it disconnects every org (`CONTEXT_DROP_SLACK_KEY` also works) |

   `CONTEXT_DROP_BASE_URL` must be the server's public HTTPS origin: Slack sends events to `<base>/v1/slack/events` and installs return to `<base>/v1/slack/oauth`.
4. Restart the server. Then, in an org, run `context-drop slack install` and approve in the browser. Each person then runs `context-drop slack connect` and signs in.

### Linking yourself

`slack connect` asks the server for a link tied to your machine, opens it, and waits until you have signed in (up to 10 minutes). The page first says which org and machine started it, so a forwarded link is easy to spot. It uses Sign in with Slack (OpenID Connect, scopes `openid profile`), which asks only for your identity; the server reads who you are, checks you're in the org's workspace, links you, and keeps no token. A link expires after 10 minutes. Use `--no-open` to print it without opening a browser.
5. Add the MCP server to agents (`claude mcp add context-drop -- context-drop mcp`) so they can message people and read Slack.

## Limits

- One workspace per org, and a workspace belongs to one org.
- Routes from threads and DMs to agents last 30 days after their last use.
- Events are handled by the one server instance, like the rest of the org endpoints.
- Bot tokens do not rotate (`token_rotation_enabled: false`).
