import assert from "node:assert/strict";
import test from "node:test";

import { parseArgv } from "../src/args.js";

test("parseArgv parses command flags and positionals", () => {
  const parsed = parseArgv([
    "launch",
    "--repo",
    "/tmp/repo",
    "--agent=codex",
    "--dry-run",
    "--",
    "extra",
  ]);

  assert.equal(parsed.command, "launch");
  assert.deepEqual(parsed.flags, {
    repo: "/tmp/repo",
    agent: "codex",
    dryRun: true,
  });
  assert.deepEqual(parsed.positionals, ["extra"]);
});

test("parseArgv reports missing values", () => {
  assert.throws(() => parseArgv(["launch", "--repo"]), /Missing value/);
});

test("parseArgv supports global flags before the command", () => {
  const parsed = parseArgv(["--config", "relaymux.json", "doctor"]);

  assert.equal(parsed.command, "doctor");
  assert.deepEqual(parsed.flags, {
    config: "relaymux.json",
  });
});

test("parseArgv recognizes notify --suicide as a boolean flag", () => {
  const parsed = parseArgv([
    "notify",
    "--suicide",
    "--reply-mode",
    "imessage",
    "--message",
    "done",
  ]);

  assert.equal(parsed.command, "notify");
  assert.equal(parsed.flags.suicide, true);
  assert.equal(parsed.flags.replyMode, "imessage");
  assert.equal(parsed.flags.message, "done");
});
