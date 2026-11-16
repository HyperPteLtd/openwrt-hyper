/** Exercise the bundled session launcher even when the OS also ships setsid. */
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { launch, stop, until, wait } from './router_test_helpers.mjs';

const binary = '/usr/libexec/hyper/bin/hyper-crypt';
if (process.platform !== 'linux' || !existsSync(binary)) {
  console.log('skip bundled session launcher (requires installed Linux router helper)');
  process.exit(0);
}
const root = mkdtempSync(join(tmpdir(), 'hyper-session-'));
let worker;
function stat(pid) {
  try { return readFileSync(`/proc/${pid}/stat`, 'utf8').split(') ').at(-1).split(' '); }
  catch { return undefined; }
}
try {
  const result = spawnSync(binary, ['run-session', 'sh', '-c', 'printf "%s:%s" "$1" "$HYPER_SESSION_VALUE"; exit 37', 'sh', 'space argument'],
    { encoding: 'utf8', env: { ...process.env, HYPER_SESSION_VALUE: 'present' } });
  assert.equal(result.status, 37, result.stderr); assert.equal(result.stdout, 'space argument:present');
  assert.equal(spawnSync(binary, ['run-session', join(root, 'missing')]).status, 127);
  const denied = join(root, 'denied'); writeFileSync(denied, '#!/bin/sh\nexit 0\n'); chmodSync(denied, 0o600);
  assert.equal(spawnSync(binary, ['run-session', denied]).status, 126);
  worker = launch(binary, ['run-session', 'sh', '-c',
    'echo "$$" > "$1/pid"; sleep 30 & echo "$!" > "$1/child"; wait', 'sh', root]);
  await until(() => existsSync(join(root, 'child')), 'session did not start');
  const pid = Number(readFileSync(join(root, 'pid'), 'utf8'));
  const child = Number(readFileSync(join(root, 'child'), 'utf8'));
  assert.equal(pid, worker.pid, 'exec changed the PID used for group cancellation');
  assert.equal(Number(stat(pid)?.[2]), pid, 'process group is not isolated');
  assert.equal(Number(stat(pid)?.[3]), pid, 'session is not isolated');
  assert.equal(Number(stat(child)?.[2]), pid, 'child escaped the process group');
  process.kill(-pid, 'SIGTERM');
  assert.equal((await wait(worker, 3000)).signal, 'SIGTERM');
  await until(() => { const childState = stat(child); return !childState || childState[0] === 'Z'; },
    'session descendant survived group cancellation');
  console.log('ok   bundled session launcher preserves PID/arguments/exit codes and terminates its complete process group');
} finally {
  if (worker?.pid) { try { process.kill(-worker.pid, 'SIGKILL'); } catch {} }
  await stop(worker);
  rmSync(root, { recursive: true, force: true });
}
