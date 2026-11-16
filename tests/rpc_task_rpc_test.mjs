/** A slow diagnostic must not occupy rpcd while the UI polls live status. */
import assert from 'node:assert/strict';
import { chmodSync, lstatSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { command, sleep } from './router_test_helpers.mjs';
assert.equal(process.env.HYPER_TEST_CONTAINER, '1', 'run only in a disposable router');
const ping = '/bin/ping';
const stat = lstatSync(ping);
const original = stat.isSymbolicLink() ? readlinkSync(ping) : readFileSync(ping);
const rpc = (method, args = {}) => JSON.parse(command('ubus', ['-t', '5', 'call', 'luci.hyper', method, JSON.stringify(args)]));
try {
  rmSync(ping);
  writeFileSync(ping, '#!/bin/sh\nsleep 2\nprintf "diagnostic-task-finished\\n"\n'); chmodSync(ping, 0o755);
  const before = performance.now();
  const ack = rpc('diag', { type: 'ping', target: 'example.com', via: 'direct' });
  assert.ok(performance.now() - before < 1500, 'slow diagnostic blocked its RPC acknowledgment');
  assert.match(ack._hyper_task, /^[0-9a-f-]{36}$/);
  assert.equal(ack._hyper_timeout, 180);
  const beganStatus = performance.now();
  rpc('status');
  assert.ok(performance.now() - beganStatus < 1500, 'status was blocked behind a background diagnostic');
  const deadline = performance.now() + 8000;
  let result;
  do {
    result = rpc('task_status', { id: ack._hyper_task });
    if (result.state !== 'running') break;
    assert.ok(performance.now() < deadline, JSON.stringify(result));
    await sleep(100);
  } while (true);
  assert.equal(result.state, 'done', JSON.stringify(result));
  assert.match(result.data.output, /diagnostic-task-finished/);
  console.log('ok   slow diagnostic acknowledges immediately and leaves live status responsive');
} finally {
  rmSync(ping);
  if (stat.isSymbolicLink()) symlinkSync(original, ping);
  else { writeFileSync(ping, original); chmodSync(ping, stat.mode); }
}
