import assert from "node:assert/strict";
import { test } from "node:test";
import { WorkspaceDirectory, workspaceInstructions } from "../src/workspaces.js";
import type { RuntimeConfig } from "../src/types.js";
function fixture() {
  const workspaces = [{ workspace_id: "project", label: "dari-mono" }, { workspace_id: "pool", label: "ContextDropManaged" }];
  const panes = [{ workspace_id: "project", pane_id: "agent", cwd: "/repo", agent: "codex", agent_status: "done", terminal_title_stripped: "Fix costs" }];
  const directory = new WorkspaceDirectory({} as RuntimeConfig, async (_program, args) => {
    assert.equal(args.at(-1), "list");
    return JSON.stringify({ result: { workspaces, panes } });
  });
  return { directory, workspaces, panes };
}
test("discovery excludes pool and resolves new work without guessing ambiguous directories", async () => {
  const { directory, panes } = fixture();
  assert.equal((await directory.discover()).length, 1);
  const target = await directory.resolve({ workspace: "dari-mono", mode: "new" });
  assert.equal(target.workspaceId, "project");
  assert.equal(target.cwd, "/repo");
  assert.match(workspaceInstructions(target), /local gwt/);
  assert.match(workspaceInstructions(target), /reuse that tab/);
  panes.push({ ...panes[0], pane_id: "other", cwd: "/other" });
  await assert.rejects(directory.resolve({ workspace: "project", mode: "new" }), /multiple directories/);
  assert.equal((await directory.resolve({ workspace: "project", mode: "new", cwd: "/other" })).cwd, "/other");
  await assert.rejects(directory.resolve({ workspace: "project", mode: "new", cwd: "/invented" }), /discovered directory/);
});
test("continuation requires exact existing agent and preserves its conversation and cwd", async () => {
  const { directory } = fixture();
  const target = await directory.resolve({ workspace: "dari-mono", mode: "continue", paneId: "agent" });
  assert.equal(target.paneId, "agent");
  assert.match(workspaceInstructions(target), /NEVER send \/new or \/clear/);
  await assert.rejects(directory.resolve({ workspace: "project", mode: "continue" }), /exact existing agent/);
  await assert.rejects(directory.resolve({ workspace: "project", mode: "continue", paneId: "foreign" }), /exact existing agent/);
  await assert.rejects(directory.resolve({ workspace: "project", mode: "continue", paneId: "agent", cwd: "/other" }), /preserves/);
});
test("unknown, duplicate, pool and conflicting targets fail closed", async () => {
  const { directory, workspaces } = fixture();
  for (const workspace of ["unknown", "pool"]) await assert.rejects(directory.resolve({ workspace, mode: "new" }), /missing or ambiguous/);
  await assert.rejects(directory.resolve({ workspace: "project", mode: "new", paneId: "agent" }), /must not target/);
  workspaces.push({ workspace_id: "duplicate", label: "dari-mono" });
  await assert.rejects(directory.resolve({ workspace: "dari-mono", mode: "new" }), /ambiguous/);
  assert.equal((await directory.resolve({ workspace: "project", mode: "new" })).workspaceId, "project");
});
