// context-drop lock: claims that hold across every machine in the org.
//   context-drop lock --path src/parser --note "rewriting the tokenizer"
//   context-drop lock --path src/parser --release
//   context-drop lock            (lists every held lock)
import { mod, z } from "@contextdrop/mod"

type Lock = { path: string; by: string; note?: string; at: string }

export default mod({
  name: "lock",
  description: "Claim a path or task across the whole org, release it, or list who holds what",
  input: z.object({
    path: z.string().optional().describe("the path or task to claim or release"),
    note: z.string().optional().describe("why you are taking it"),
    release: z.boolean().default(false).describe("release it instead"),
    force: z.boolean().default(false).describe("take it even if someone else holds it"),
  }),
  can: ["kv", "list"],
  async run({ path, note, release, force }, cd) {
    const held = ((await cd.kv.get("locks")) ?? {}) as Record<string, Lock>
    if (!path) return Object.values(held)
    const me = cd.caller.kind === "agent" ? cd.caller.agent : cd.caller.kind === "machine" ? cd.caller.machine : "event"
    // Locks held by agents that have ended are free.
    const live = new Set((await cd.list({})).map((a) => a.id))
    for (;;) {
      const before = ((await cd.kv.get("locks")) ?? null) as Record<string, Lock> | null
      const locks = { ...(before ?? {}) }
      for (const [p, l] of Object.entries(locks)) if (l.by.includes("/") && !live.has(l.by)) delete locks[p]
      const cur = locks[path]
      if (release) {
        if (!cur) return `${path} is not locked`
        if (cur.by !== me && !force) throw new Error(`${path} is held by ${cur.by}; pass --force to release it anyway`)
        delete locks[path]
      } else {
        if (cur && cur.by !== me && !force) throw new Error(`${path} is held by ${cur.by} since ${cur.at}${cur.note ? `: ${cur.note}` : ""}`)
        locks[path] = { path, by: me, note, at: new Date().toISOString() }
      }
      if (await cd.kv.cas("locks", before ?? undefined, locks)) return release ? `released ${path}` : `${me} holds ${path}`
    }
  },
})
