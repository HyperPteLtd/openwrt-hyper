/** Run only inside the disposable router test image with its real rpcd. */
import assert from 'node:assert/strict';
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { command, sleep } from './router_test_helpers.mjs';
const init = '/etc/init.d/hyper';
const original = readFileSync(init);
const sessionPath = '/etc/hyper/session';
const session = existsSync(sessionPath) ? readFileSync(sessionPath) : null;
function rpc(method, args = {}) { return JSON.parse(command('ubus', ['-t', '5', 'call', 'luci.hyper', method, JSON.stringify(args)])); }
try {
  writeFileSync(init, '#!/bin/sh\nsleep 2\nexit 0\n');
  let begin = performance.now();
  const result = rpc('set_enabled', { enabled: false });
  const elapsed = (performance.now() - begin) / 1000;
  assert(result.result && result.accepted, JSON.stringify(result));
  assert(elapsed < 1.5, `control RPC blocked for ${elapsed.toFixed(2)}s`);
  assert(['queued', 'running'].includes(result.operation.state), JSON.stringify(result));
  begin = performance.now();
  let status = rpc('status');
  assert(performance.now() - begin < 1500, 'status blocked behind the service operation');
  assert.equal(status.operation.id, result.operation.id);
  const deadline = performance.now() + 8000;
  while (['queued', 'running'].includes(status.operation.state)) {
    assert(performance.now() < deadline, JSON.stringify(status));
    await sleep(100);
    status = rpc('status');
  }
  assert.equal(status.operation.state, 'done', JSON.stringify(status));
  console.log(`ok   control RPC acknowledged in ${elapsed.toFixed(3)}s; status stays responsive until completion`);
  rmSync(sessionPath, { force: true });
  const rejected = rpc('set_enabled', { enabled: true });
  assert.equal(rejected.result, false);
  assert.equal(rejected.error, 'not signed in');
  assert.equal(command('uci', ['-q', 'get', 'hyper.config.enabled']).trim(), '0');
  assert.equal(rpc('status').operation.id, result.operation.id);
  console.log('ok   delayed Start after logout cannot enable the service or enqueue another operation');
} finally {
  writeFileSync(init, original);
  if (session) writeFileSync(sessionPath, session, { mode: 0o600 });
  else rmSync(sessionPath, { force: true });
}
