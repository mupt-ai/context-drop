// Runs one mod call. The server writes an entry file that imports the mod and this runner, then passes
// the call on stdin. The result is the last line on stdout; the mod's own console output goes to stderr.

import type { ContextDrop, ModDefinition } from "./mod.ts"

type Invocation = {
  mode: "describe" | "run"
  input?: Record<string, unknown>
  event?: Record<string, unknown>
  caller?: ContextDrop["caller"]
  api?: { base: string; org: string; token: string }
  secrets?: Record<string, string>
}

const out = (v: unknown) => Deno.stdout.writeSync(new TextEncoder().encode("\n" + JSON.stringify(v) + "\n"))
for (const level of ["log", "info", "warn", "error", "debug"] as const) {
  console[level] = (...args: unknown[]) =>
    Deno.stderr.writeSync(new TextEncoder().encode(args.map((a) => (typeof a === "string" ? a : Deno.inspect(a))).join(" ") + "\n"))
}

function client(inv: Invocation): ContextDrop {
  const api = inv.api!
  async function post(path: string, body: unknown) {
    const r = await fetch(`${api.base}/v1/${api.org}/${path}`, {
      method: "POST",
      headers: { authorization: `Bearer ${api.token}`, "content-type": "application/json" },
      body: JSON.stringify(body ?? {}),
    })
    const data = await r.json().catch(() => ({}))
    if (!r.ok) throw new Error(data?.error?.message ?? `${path} failed: HTTP ${r.status}`)
    return data
  }
  return {
    caller: inv.caller!,
    list: (selector = {}) => post("list", selector),
    read: (id, options = {}) => post("read", { id, ...options }),
    send: (target, text) => post("send", { target: typeof target === "string" ? { id: target } : target, text }),
    call: async (name, input = {}) => (await post(`mods/${name}/call`, { input })).output,
    kv: {
      get: async (key) => (await post("kv", { op: "get", key })).value,
      set: async (key, value) => void (await post("kv", { op: "set", key, value })),
      cas: async (key, expected, value) => (await post("kv", { op: "cas", key, expected, value })).written,
    },
    secrets: { get: (name) => inv.secrets?.[name] },
  }
}

export async function main(def: ModDefinition<unknown>) {
  const inv: Invocation = JSON.parse(await new Response(Deno.stdin.readable).text())
  try {
    if (!def || typeof def.run !== "function" || typeof def.name !== "string") {
      throw new Error("the file must `export default mod({ name, description, run, ... })`")
    }
    if (inv.mode === "describe") {
      out({
        ok: true,
        output: {
          name: def.name,
          description: def.description ?? "",
          input: def.input ? def.input.toJSON() : { type: "object", properties: {}, required: [] },
          can: def.can ?? [],
          on: def.on ?? [],
          webhook: def.webhook ?? null,
        },
      })
      return
    }
    const input = def.input ? def.input.parse(inv.input ?? {}) : inv.input ?? {}
    const result = await def.run(input, client(inv), inv.event as never)
    out({ ok: true, output: result === undefined ? null : result })
  } catch (e) {
    out({ ok: false, error: e instanceof Error ? e.message : String(e) })
  }
}
