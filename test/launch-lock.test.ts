import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { withScheduleLock, scheduleLockPath } from "../src/launch/lock.js";

test("scheduleLockPath derives a safe lock directory from the schedule/window name", () => {
  const stateDir = "/tmp/state";
  assert.equal(
    scheduleLockPath(stateDir, "personal-digest-hourly", "personal-digest-hourly"),
    path.join(stateDir, "locks", "schedule-personal-digest-hourly.lock"),
  );
  // unsafe characters are collapsed
  assert.equal(
    scheduleLockPath(stateDir, "nightly.build", "nightly-build-e2ee6b"),
    path.join(stateDir, "locks", "schedule-nightly-build-e2ee6b.lock"),
  );
});

test("withScheduleLock runs the critical section and releases the lock", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-lock-basic-"));
  const stateDir = path.join(root, "state");
  let ran = false;
  const result = withScheduleLock(
    { stateDir, scheduleName: "daily", windowName: "daily" },
    () => {
      ran = true;
      assert.equal(fs.existsSync(scheduleLockPath(stateDir, "daily", "daily")), true);
      return "done";
    },
  );
  assert.equal(ran, true);
  assert.equal(result, "done");
  assert.equal(fs.existsSync(scheduleLockPath(stateDir, "daily", "daily")), false);
});

test("withScheduleLock without a schedule name does not lock", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-lock-none-"));
  const stateDir = path.join(root, "state");
  let ran = false;
  withScheduleLock({ stateDir, scheduleName: "", windowName: "" }, () => { ran = true; });
  assert.equal(ran, true);
  assert.equal(fs.existsSync(path.join(stateDir, "locks")), false);
});

test("withScheduleLock reclaims a stale lock older than the TTL", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-lock-stale-"));
  const stateDir = path.join(root, "state");
  const lockDir = scheduleLockPath(stateDir, "daily", "daily");
  fs.mkdirSync(path.dirname(lockDir), { recursive: true });
  fs.mkdirSync(lockDir);
  fs.writeFileSync(path.join(lockDir, "owner"), "999999");

  // Backdate the lock directory so it is older than the TTL.
  const stale = new Date(Date.now() - 120_000);
  fs.utimesSync(lockDir, stale, stale);

  let ran = false;
  withScheduleLock(
    { stateDir, scheduleName: "daily", windowName: "daily", timeoutMs: 200, staleTtlMs: 60_000 },
    () => { ran = true; },
  );
  assert.equal(ran, true);
  assert.equal(fs.existsSync(lockDir), false);
});

test("withScheduleLock serializes overlapping critical sections", async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-lock-serial-"));
  const stateDir = path.join(root, "state");
  const lockDir = scheduleLockPath(stateDir, "daily", "daily");
  const order: string[] = [];

  // Simulate two "processes" by using setTimeout-based critical sections and
  // awaiting both. The second must wait until the first releases.
  const run = (label: string, holdMs: number) => new Promise<void>((resolve) => {
    withScheduleLock({ stateDir, scheduleName: "daily", windowName: "daily", timeoutMs: 2000, pollMs: 10 }, () => {
      order.push(`${label}:enter`);
      // Busy-wait synchronously to hold the lock; use a synchronous spin so the
      // mkdir lock stays held across this tick.
      const end = Date.now() + holdMs;
      while (Date.now() < end) { /* hold */ }
      order.push(`${label}:exit`);
    });
    // withScheduleLock is synchronous; resolve on next tick.
    setImmediate(resolve);
  });

  const p1 = run("a", 60);
  const p2 = run("b", 10);
  await Promise.all([p1, p2]);

  // Both entered and exited, and b did not enter while a held the lock.
  assert.deepEqual(order, ["a:enter", "a:exit", "b:enter", "b:exit"]);
  assert.equal(fs.existsSync(lockDir), false);
});
