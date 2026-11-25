// @contextdrop/mod: what a mod imports.
//
//   import { mod, z } from "@contextdrop/mod"
//   export default mod({ name, description, input, can, run(input, cd) { ... } })

export type Capability = "list" | "read" | "send" | "call" | "kv" | "slack"

export type SlackMessage = {
  ts: string
  user?: string
  name?: string
  bot?: boolean
  text: string
  thread_ts?: string
  replies?: number
  // The message's thread as a send target, "slack:CHANNEL/TS".
  ref: string
}

export type Caller =
  | { kind: "machine"; machine: string }
  | { kind: "agent"; machine: string; agent: string }
  | { kind: "mod"; mod: string }

export type ContextDrop = {
  caller: Caller
  list(selector?: Record<string, unknown>): Promise<Record<string, unknown>[]>
  read(id: string, options?: { last?: number; all?: boolean; after?: string }): Promise<Record<string, unknown>>
  // A target of "#channel", "@person", or "slack:CHANNEL/TS" posts to Slack (needs "slack" in can).
  send(target: string | Record<string, unknown>, text: string): Promise<Record<string, unknown>>
  // The org's Slack workspace, with the app's bot token (needs "slack" in can).
  slack: {
    // Any allowed Web API method: chat.*, conversations.*, reactions.*, pins.*, bookmarks.*, users.info, ...
    api(method: string, args?: Record<string, unknown>): Promise<Record<string, unknown>>
    // Messages in "#channel", "@person" (their DM with the app), or a "slack:CHANNEL/TS" thread, oldest first.
    read(target: string, options?: { limit?: number; oldest?: string }): Promise<{ channel: string; messages: SlackMessage[] }>
  }
  call(name: string, input?: Record<string, unknown>): Promise<unknown>
  kv: {
    get(key: string): Promise<unknown>
    set(key: string, value: unknown): Promise<void>
    // cas writes value only if the key still holds expected (undefined = absent); returns whether it wrote.
    cas(key: string, expected: unknown, value: unknown): Promise<boolean>
  }
}

export type ModDefinition<I> = {
  name: string
  description: string
  input?: Schema<I>
  can?: Capability[]
  run(input: I, cd: ContextDrop): unknown | Promise<unknown>
}

export function mod<I>(definition: ModDefinition<I>): ModDefinition<I> {
  return definition
}

// A small schema builder: validates input and describes it as JSON Schema, so the CLI can turn flags
// into input and agents see each mod as a typed tool.

type JSONSchema = Record<string, unknown>

export class Schema<T> {
  constructor(
    readonly check: (value: unknown, path: string) => T,
    readonly json: JSONSchema,
    readonly isOptional = false,
    readonly fallback?: unknown,
  ) {}

  parse(value: unknown, path = "input"): T {
    if (value === undefined) {
      if (this.fallback !== undefined) return structuredClone(this.fallback) as T
      if (this.isOptional) return undefined as T
      throw new Error(`${path} is required`)
    }
    return this.check(value, path)
  }

  optional(): Schema<T | undefined> {
    return new Schema<T | undefined>(this.check, this.json, true, this.fallback)
  }

  default(value: T): Schema<T> {
    return new Schema<T>(this.check, { ...this.json, default: value }, true, value)
  }

  describe(description: string): Schema<T> {
    return new Schema<T>(this.check, { ...this.json, description }, this.isOptional, this.fallback)
  }

  toJSON(): JSONSchema {
    return this.json
  }
}

function fail(path: string, want: string, value: unknown): never {
  throw new Error(`${path} must be ${want}, got ${JSON.stringify(value)}`)
}

export const z = {
  string: () => new Schema<string>((v, p) => (typeof v === "string" ? v : fail(p, "a string", v)), { type: "string" }),
  number: () => new Schema<number>((v, p) => (typeof v === "number" && Number.isFinite(v) ? v : fail(p, "a number", v)), { type: "number" }),
  boolean: () => new Schema<boolean>((v, p) => (typeof v === "boolean" ? v : fail(p, "true or false", v)), { type: "boolean" }),
  any: () => new Schema<unknown>((v) => v, {}),
  enum: <const V extends readonly string[]>(values: V) =>
    new Schema<V[number]>((v, p) => ((values as readonly unknown[]).includes(v) ? (v as V[number]) : fail(p, `one of ${values.join(", ")}`, v)), {
      type: "string",
      enum: [...values],
    }),
  array: <T>(item: Schema<T>) =>
    new Schema<T[]>((v, p) => (Array.isArray(v) ? v.map((x, i) => item.parse(x, `${p}[${i}]`)) : fail(p, "an array", v)), {
      type: "array",
      items: item.toJSON(),
    }),
  object: <S extends Record<string, Schema<unknown>>>(shape: S) =>
    new Schema<{ [K in keyof S]: S[K] extends Schema<infer T> ? T : never }>(
      (v, p) => {
        if (typeof v !== "object" || v === null || Array.isArray(v)) fail(p, "an object", v)
        const out: Record<string, unknown> = {}
        for (const [k, s] of Object.entries(shape)) {
          const parsed = s.parse((v as Record<string, unknown>)[k], `${p}.${k}`)
          if (parsed !== undefined) out[k] = parsed
        }
        return out as { [K in keyof S]: S[K] extends Schema<infer T> ? T : never }
      },
      {
        type: "object",
        properties: Object.fromEntries(Object.entries(shape).map(([k, s]) => [k, s.toJSON()])),
        required: Object.entries(shape).filter(([, s]) => !s.isOptional).map(([k]) => k),
      },
    ),
}
