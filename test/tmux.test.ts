import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { killCurrentWindow } from "../src/tmux.js";

function makeFakeTmux(dir: string, body: string) {
  const file = path.join(dir, "tmux");
  fs.writeFileSync(file, `#!/bin/sh\n${body}\n`, { mode: 0o755 });
  return file;
}

test("killCurrentWindow does not resolve or kill without a tmux environment", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-tmux-outside-"));
  const log = path.join(dir, "tmux.log");
  makeFakeTmux(dir, `echo "$*" >> ${JSON.stringify(log)}; exit 0`);

  const result = killCurrentWindow({ env: { PATH: dir } });

  assert.equal(result.killed, false);
  assert.match(result.error, /not inside tmux/);
  assert.equal(fs.existsSync(log), false);
});

test("killCurrentWindow resolves the window from the current tmux pane before killing", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relaymux-tmux-current-"));
  const log = path.join(dir, "tmux.log");
  makeFakeTmux(dir, `
echo "$*" >> ${JSON.stringify(log)}
if [ "$1" = "display-message" ]; then
  printf 'agents:3\n'
  exit 0
fi
exit 0
`);

  const result = killCurrentWindow({
    env: {
      PATH: dir,
      TMUX: "/tmp/tmux-test/default,1,0",
      TMUX_PANE: "%9",
    },
  });

  assert.deepEqual(result, { killed: true, target: "agents:3" });
  assert.deepEqual(fs.readFileSync(log, "utf8").trim().split("\n"), [
    "display-message -p -t %9 #S:#I",
    "kill-window -t agents:3",
  ]);
});
