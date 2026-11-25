# Mods

A mod is a command an org adds to Context Drop. It is one TypeScript file, deployed to the org's server, and it behaves like a built-in command: `context-drop <name>` runs it, agents see it as a tool, and anything with a credential can call it over HTTP. Mods run on the server, never on the org's machines, and can do only what their `can` list grants.

```ts
// stuck.ts
import { mod, z } from "@contextdrop/mod"

export default mod({
  name: "stuck",
  description: "Agents that are blocked",
  input: z.object({ machine: z.string().optional() }),
  can: ["list"],
  async run({ machine }, cd) {
    return (await cd.list({ machine })).filter((a) => a.status === "blocked")
  },
})
```

```bash
context-drop deploy stuck.ts        # live for the org at once
context-drop stuck --machine devbox # call it like any command
```

Two reference mods are in [`mods/`](../mods): `stuck` and `lock` (an org-wide lock on a path).

## Writing one

`mod({...})` takes:

| Field | What it is |
|---|---|
| `name` | The command name: lowercase letters, digits, and dashes. A built-in name always wins over a mod's. |
| `description` | One line, shown in `context-drop mods` and to agents. |
| `input` | A schema built with `z` (below). The CLI turns flags into this input; agents see it as the tool's parameters. |
| `can` | The capabilities it uses. Anything not listed fails. |
| `run(input, cd)` | Does the work and returns the result: text prints as is, a list of objects as a table, anything else as JSON. |

`z` covers `z.string()`, `z.number()`, `z.boolean()`, `z.enum([...])`, `z.array(x)`, `z.object({...})`, and `z.any()`, each with `.optional()`, `.default(v)`, and `.describe("...")`. A mod is one file; it cannot import anything but `@contextdrop/mod`.

## What a mod can use

`cd` is the mod's handle on the org:

| `cd.` | Does | Capability |
|---|---|---|
| `list(selector)`, `read(id, options)`, `send(target, text)` | The built-in endpoints, with the same inputs and outputs. A message a mod sends names the mod as its sender. | The same name |
| `call(name, input)` | Call another mod (up to 4 deep) | `call` |
| `kv.get(key)`, `kv.set(key, value)`, `kv.cas(key, expected, value)` | The mod's own key-value store (10 MB). `cas` writes only if the key still holds `expected` (`undefined` = absent), for locks and counters. Setting `null` deletes. | `kv` |
| `send("#channel" \| "@person" \| "slack:C/TS", text)`, `slack.read(target, options)`, `slack.api(method, args)` | The org's [Slack](slack.md) workspace: post, read, and call allowed Web API methods with its bot token | `slack` |
| `caller` | Who called: `{kind: "agent", agent}`, `{kind: "machine", machine}`, or `{kind: "mod", mod}` | Always |

## Managing mods

```bash
context-drop deploy FILE    # add the mod, or replace the one with the same name
context-drop mods           # every mod with its capabilities and author
context-drop call NAME [--FIELD VALUE]... [--json INPUT] [--print-json]
```

`context-drop NAME ...` is the same as `context-drop call NAME ...` when NAME is not a built-in command. Deploying again replaces the mod; its key-value data stays.

## Agents

`context-drop mcp` is an MCP server that gives an agent `list`, `read`, `send`, `ask`, and every deployed mod as tools, and tells the agent when the list changes, so a new mod reaches it without a restart:

```bash
claude mcp add context-drop -- context-drop mcp
```

## How it runs

Each call runs the mod in its own [Deno](https://deno.com) process with no file, environment, or subprocess access, network access only to the server itself, a 256 MB heap, and a 15-minute limit. The mod reaches the org through the server's own endpoints with a token that is valid only while the call runs and only for its `can` list. The server needs Deno installed (`CONTEXT_DROP_DENO`, or `deno` on its PATH); without it, mods are off.
