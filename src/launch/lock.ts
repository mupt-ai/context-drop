import fs from "node:fs";
import path from "node:path";

import { ensureDirectory } from "../paths.js";

// Best-effort critical-section lock so two simultaneous schedule ticks (or a
// tick racing a manual relaunch) cannot both create a tmux window for the same
// schedule. Uses atomic mkdir as the lock primitive with a short bounded retry.
//
// This is local single-machine coordination only; it is not a distributed lock.
// A stale lock directory older than the TTL is reclaimed so a crashed process
// does not wedge the schedule forever.

const DEFAULT_TIMEOUT_MS = 5000;
const DEFAULT_POLL_MS = 25;
const DEFAULT_STALE_TTL_MS = 60_000;

export function withScheduleLock({ stateDir, scheduleName, windowName, timeoutMs = DEFAULT_TIMEOUT_MS, pollMs = DEFAULT_POLL_MS, staleTtlMs = DEFAULT_STALE_TTL_MS, now = Date.now }, fn) {
  if (!scheduleName) return fn();
  const lockDir = scheduleLockPath(stateDir, scheduleName, windowName);
  ensureDirectory(path.dirname(lockDir));
  const deadline = now() + timeoutMs;
  let acquired = false;
  try {
    while (now() < deadline) {
      if (tryAcquireLock(lockDir, staleTtlMs, now)) {
        acquired = true;
        break;
      }
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, pollMs);
    }
    if (!acquired) {
      // Last resort: proceed without the lock rather than dropping the launch.
      // The window-replacement logic still targets the stable schedule window,
      // so a rare race can at worst produce a transient duplicate that the next
      // tick reaps.
    }
    return fn();
  } finally {
    if (acquired) releaseLock(lockDir);
  }
}

export function scheduleLockPath(stateDir, scheduleName, windowName) {
  const safe = String(windowName || scheduleName)
    .replace(/[^A-Za-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 80) || "schedule";
  return path.join(stateDir, "locks", `schedule-${safe}.lock`);
}

function tryAcquireLock(lockDir, staleTtlMs, now) {
  try {
    fs.mkdirSync(lockDir);
    fs.writeFileSync(path.join(lockDir, "owner"), String(process.pid));
    return true;
  } catch (error) {
    if (error?.code !== "EEXIST") throw error;
    // Reclaim stale locks from crashed processes.
    try {
      const stat = fs.statSync(lockDir);
      if (now() - stat.mtimeMs > staleTtlMs) {
        fs.rmSync(lockDir, { recursive: true, force: true });
        try {
          fs.mkdirSync(lockDir);
          fs.writeFileSync(path.join(lockDir, "owner"), String(process.pid));
          return true;
        } catch {
          return false;
        }
      }
    } catch {}
    return false;
  }
}

function releaseLock(lockDir) {
  try {
    fs.rmSync(lockDir, { recursive: true, force: true });
  } catch {}
}
