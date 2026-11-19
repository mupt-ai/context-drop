// context-drop stuck: agents waiting on someone, longest first.
import { mod, z } from "@contextdrop/mod"

export default mod({
  name: "stuck",
  description: "Agents that have been blocked (waiting for an approval or an answer) for at least N minutes",
  input: z.object({ minutes: z.number().default(10).describe("blocked at least this long") }),
  can: ["list"],
  async run({ minutes }, cd) {
    const now = Date.now()
    return (await cd.list({}))
      .filter((a) => a.status === "blocked")
      .map((a) => ({ id: a.id, for_min: Math.round((now - Date.parse(String(a.last_active))) / 60000), cwd: a.cwd, name: a.name }))
      .filter((a) => a.for_min >= minutes)
      .sort((a, b) => b.for_min - a.for_min)
  },
})
