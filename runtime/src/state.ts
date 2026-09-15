import { chmodSync, closeSync, fsyncSync, openSync, renameSync, writeFileSync } from "node:fs";

export function writePrivate(path: string, value: unknown): void {
  const tmp = path + ".tmp";
  writeFileSync(tmp, JSON.stringify(value), { mode: 0o600 });
  chmodSync(tmp, 0o600);
  const fd = openSync(tmp, "r");
  try { fsyncSync(fd); } finally { closeSync(fd); }
  renameSync(tmp, path);
}
