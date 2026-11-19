# Mods

A mod is a command an org adds to Context Drop. It is one TypeScript file, deployed to the org's server, and it behaves like a built-in command: `context-drop <name>` runs it, agents see it as a tool, and anything with a credential can call it over HTTP. Mods run on the server, never on the org's machines, and can do only what their `can` list grants.

```ts
// stuck.ts
import { mod, z } from "@contextdrop/mod"

export default mod({
  name: "stuck",
  description: "Agents blocked for at least N minutes",
  input: z.object({ minutes: z.number().default(10) }),
  can: ["list"],
  async run({ minutes }, cd) {
    const agents = await cd.list({})
    return agents.filter((a) => a.status === "blocked")
  },
})
```

```bash
context-drop deploy stuck.ts      # live for the org at once
context-drop stuck --minutes 5    # call it like any command
```

Two reference mods are in [`mods/`](../mods): `stuck` and `lock` (claims that hold across every machine).

## Writing one

`mod({...})` takes:

| Field | What it is |
|---|---|
| `name` | The command name: lowercase letters, digits, and dashes. A built-in name always wins over a mod's. |
| `description` | One line, shown in `context-drop mods` and to agents. |
| `input` | A schema built with `z` (below). The CLI turns flags into this input; agents see it as the tool's parameters. |
| `can` | The capabilities it uses. Anything not listed fails. |
| `on` | Events that call it (below). |
| `webhook` | `{ verify: "github" \| "slack" \| "hmac" }` gives it a public URL. |
| `run(input, cd, event)` | Does the work and returns the result: text prints as is, a list of objects as a table, anything else as JSON. |

`z` covers `z.string()`, `z.number()`, `z.boolean()`, `z.enum([...])`, `z.array(x)`, `z.object({...})`, and `z.any()`, each with `.optional()`, `.default(v)`, and `.describe("...")`. A mod is one file; it cannot import anything but `@contextdrop/mod`.

## What a mod can use

`cd` is the mod's handle on the org:

| `cd.` | Does | Capability |
|---|---|---|
| `list(selector)`, `read(id, options)`, `send(target, text)` | The built-in endpoints, with the same inputs and outputs. A message a mod sends names the mod as its sender. | The same name |
| `call(name, input)` | Call another mod (up to 4 deep) | `call` |
| `kv.get(key)`, `kv.set(key, value)`, `kv.cas(key, expected, value)` | The mod's own key-value store (10 MB). `cas` writes only if the key still holds `expected` (`undefined` = absent), for locks and counters. Setting `null` deletes. | `kv` |
| `secrets.get(name)` | A secret set with `context-drop secret set <mod> <name>` | `secret:<name>` |
| `fetch(url)` | Outbound HTTP to that host | `net:<host>` |
| `caller` | Who called: `{kind: "agent", agent}`, `{kind: "machine", machine}`, `{kind: "mod", mod}`, or `{kind: "event"}` | Always |

`ask`, `summarize`, and the shared folder are not available yet; a mod that lists them is refused at deploy.

## Events

`on` names the events that call the mod; the event is `run`'s third argument. Delivery is at least once, so treat a repeated `event.id` as handled.

| Event | When |
|---|---|
| `agent.started`, `agent.ended` | An agent appears or ends (`event.agent`) |
| `agent.status` | An agent's status changes (`event.from`, `event.to`) |
| `schedule:<cron>` | Five-field cron, in UTC, e.g. `schedule:0 9 * * 1-5` |
| `webhook` | An outside service posts to the mod's webhook URL |

## Webhooks

A mod with `webhook` and `on: ["webhook"]` gets `https://<server>/hooks/<org>/<name>`. Set its secret with `context-drop secret set <name> webhook`; the server checks every request's signature with it before calling the mod (`X-Hub-Signature-256` for github, `X-Slack-Signature` for slack, `X-Signature-256: sha256=<hmac>` for hmac), so unsigned requests never reach it. The event carries `headers` and `body`; what the mod returns is the response.

## Managing mods

```bash
context-drop deploy FILE [--preview] [--yes]   # a new version; --preview deploys <name>@<this machine>, callable only from here
context-drop mods                              # every mod with its version, capabilities, events, and author
context-drop rollback NAME                     # the previous version is live again
context-drop logs NAME [-v]                    # recent calls, errors, and (with -v) what each printed
context-drop secret set NAME KEY               # reads the value from standard input
context-drop call NAME [--FIELD VALUE]... [--json INPUT] [--print-json]
```

Every deploy is a version. A version that adds a capability lists the new ones and asks to confirm (or `--yes`). `context-drop NAME ...` is the same as `context-drop call NAME ...` when NAME is not a built-in command.

## Agents

`context-drop mcp` is an MCP server that gives an agent `list`, `read`, `send`, and every deployed mod as tools, and tells the agent when the list changes, so a new mod reaches it without a restart:

```bash
claude mcp add context-drop -- context-drop mcp
```

## How it runs

Each call runs the mod in its own [Deno](https://deno.com) process with no file, environment, or subprocess access, network access only to the server and the hosts its `net:` capabilities name, a 256 MB heap, and a 15-minute limit. The mod reaches the org through the server's own endpoints with a token that is valid only while the call runs and only for its `can` list. The server needs Deno installed (`CONTEXT_DROP_DENO`, or `deno` on its PATH); without it, mods are off.
