import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, copyFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

function runNamer(t, { local, path, override }) {
  const dir = mkdtempSync(join(tmpdir(), 'herdr-namer-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const script = join(dir, 'namer.mjs');
  copyFileSync(new URL('./namer.mjs', import.meta.url), script);
  function client(subdir, body) {
    const binDir = join(dir, subdir);
    mkdirSync(binDir, { recursive: true });
    const bin = join(binDir, 'herdr');
    writeFileSync(bin, `#!${process.execPath}\n${body}\n`, { mode: 0o755 });
    return bin;
  }
  const env = { ...process.env, HOME: dir, HERDR_ENV: '1' };
  delete env.HERDR_BIN;
  delete env.NAMER_INTERVAL;
  if (local) client('.local/bin', local);
  if (path) {
    client('path-bin', path);
    env.PATH = join(dir, 'path-bin');
  }
  if (override) env.HERDR_BIN = client('override-bin', override);
  return spawnSync(process.execPath, [script, '--once', '--dry-run'], { env, encoding: 'utf8', timeout: 10000 });
}

const empty = `console.log(JSON.stringify({result:{workspaces:[]}}));`;
const incompatible = `console.error('client protocol 19 is older than server protocol 22'); process.exit(1);`;

test('prefers the user-installed client over an incompatible PATH client', (t) => {
  const result = runNamer(t, { local: empty, path: incompatible });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /found 0 tabs/);
  assert.doesNotMatch(result.stderr, /protocol 19/);
});

test('respects an explicit HERDR_BIN', (t) => {
  const result = runNamer(t, { local: incompatible, override: empty });
  assert.equal(result.status, 0, result.stderr);
});

test('falls back to PATH when no user-installed client exists', (t) => {
  const result = runNamer(t, { path: empty });
  assert.equal(result.status, 0, result.stderr);
});

for (const [name, body] of [
  ['protocol mismatch', incompatible],
  ['error envelope', `console.log(JSON.stringify({error:{code:'protocol_mismatch'}}));`],
  ['malformed JSON', `console.log('not JSON');`],
  ['missing workspaces', `console.log('{}');`],
  ['invalid tab discovery', `console.log(JSON.stringify(process.argv[2] === 'workspace' ? {result:{workspaces:[{workspace_id:'test-workspace'}]}} : {result:{}}));`],
]) {
  test(`${name} fails the process instead of reporting a successful empty pass`, (t) => {
    const result = runNamer(t, { override: body });
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, /pass failed:/);
    assert.doesNotMatch(result.stderr, /pass done:|found 0 tabs/);
  });
}
