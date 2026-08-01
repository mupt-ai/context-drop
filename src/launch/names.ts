import { randomUUID } from "node:crypto";
import { createHash } from "node:crypto";

export function sanitizeLaunchName(value) {
  return String(value)
    .trim()
    .replace(/[^A-Za-z0-9_.-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 80) || "agent";
}

export function makeRunId() {
  return `run-${Date.now().toString(36)}-${randomUUID().slice(0, 8)}`;
}

// tmux window names live in the `session:name` target namespace, where a `.` or
// `:` in the name would be parsed as a pane/index separator. Schedule names are
// valid identifiers but may contain `.`, so map the schedule name to a safe
// stable tmux window name while keeping it recognizable. When sanitization
// changes the name (information loss, e.g. dots collapsed to dashes), append a
// short hash so two distinct schedules cannot collapse onto the same window.
export function scheduleWindowName(scheduleName) {
  const original = String(scheduleName || "").trim();
  const sanitized = original
    .replace(/[^A-Za-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 72)
    .replace(/^-+|-+$/g, "") || "schedule";
  if (sanitized === original || !original) return sanitized;
  const hash = createHash("sha1").update(original).digest("hex").slice(0, 6);
  return `${sanitized.slice(0, 64)}-${hash}`;
}

export function sanitizeScheduleName(value) {
  const name = String(value || "").trim();
  if (!name) {
    throw new Error("Missing schedule name");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(name)) {
    throw new Error(`Invalid schedule name "${name}". Use letters, numbers, dots, underscores, and dashes, starting with a letter or number.`);
  }
  return name;
}

