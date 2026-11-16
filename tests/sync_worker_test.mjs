/** Real async RPC/worker lock with deterministic synchronization work. */
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { command, sleep, until } from './router_test_helpers.mjs';
assert.equal(process.env.HYPER_TEST_CONTAINER, '1');
const root = mkdtempSync(join(tmpdir(), 'hyper-sync-test-'));
const script = '/etc/hyper/scripts/sync_subscription.uc';
const original = readFileSync(script);
const job = '/var/run/hyper-sync';
const statePath = '/var/run/hyper/sync.json';
const session = '/etc/hyper/session';
const previousSession = existsSync(session) ? readFileSync(session) : undefined;
const previousState = existsSync(statePath) ? readFileSync(statePath) : undefined;
function rpc(method, args = {}) {
  return JSON.parse(command('ucode', ['-S', '-e', `const m=loadfile('/usr/share/rpcd/ucode/luci.hyper')()['luci.hyper']; printf('%J',m.${method}.call({args:${JSON.stringify(args)}}));`]));
}
try {
  mkdirSync('/var/run/hyper', { recursive: true });
  writeFileSync(session, JSON.stringify({ jwt: 'fixture-one' }));
  writeFileSync(script, `#!/bin/sh
mkdir '${root}/active' || { echo overlap >> '${root}/calls'; exit 1; }
echo run >> '${root}/calls'
touch '${root}/started'
while [ ! -f '${root}/gate' ]; do ucode -e 'sleep(50);'; done
rmdir '${root}/active'
if [ -f '${root}/fail' ]; then
  echo '{"ok":false,"error":"fixture"}' > '${statePath}'
  exit 1
fi
echo '{"ok":true,"time":1,"count":8}' > '${statePath}'
`);
  const start = performance.now(); const first = rpc('sync_subscription');
  assert(first.accepted && first.pending && first.id);
  assert(performance.now() - start < 1000, 'sync RPC waited for the download');
  await until(() => existsSync(join(root, 'started')), 'background sync did not start');
  assert.equal(rpc('sync_subscription').id, first.id, 'repeated refresh should coalesce');
  command('/etc/hyper/scripts/sync_worker.sh');
  assert.equal(readFileSync(join(root, 'calls'), 'utf8'), 'run\n', 'cron must not overlap or duplicate active refresh');
  const statusBegin = performance.now(); rpc('status');
  assert(performance.now() - statusBegin < 1000, 'status blocked behind background sync');
  writeFileSync(session, JSON.stringify({ jwt: 'fixture-two' }));
  const nextAccount = rpc('sync_subscription');
  assert.notEqual(nextAccount.id, first.id, 'a new account must receive a fresh sync job');
  assert.equal(rpc('sync_status').id, nextAccount.id);
  assert.equal(readFileSync(join(root, 'calls'), 'utf8'), 'run\n');
  writeFileSync(join(root, 'gate'), '');
  await until(() => !rpc('sync_status').pending, 'sync completion not published');
  assert(rpc('sync_status').result);
  assert.equal(rpc('sync_status').id, nextAccount.id);
  assert.equal(readFileSync(join(root, 'calls'), 'utf8'), 'run\nrun\n');
  console.log('ok   node refresh returns immediately; duplicate requests and cron share one background sync');

  writeFileSync(join(root, 'fail'), '');
  const next = rpc('sync_subscription'); assert.notEqual(next.id, first.id);
  await until(() => !rpc('sync_status').pending, 'failed sync remained pending');
  assert.equal(rpc('sync_status').result, false);
  assert.equal(rpc('sync_status').state.error, 'fixture');
  assert.equal(readFileSync(join(root, 'calls'), 'utf8'), 'run\nrun\nrun\n');
  console.log('ok   failed background refresh publishes its own terminal state without stale success');
} finally {
  writeFileSync(join(root, 'gate'), ''); await sleep(200);
  if (previousSession) writeFileSync(session, previousSession); else rmSync(session, { force: true });
  writeFileSync(script, original); rmSync(job, { recursive: true, force: true });
  if (previousState) writeFileSync(statePath, previousState); else rmSync(statePath, { force: true });
  rmSync(root, { recursive: true, force: true });
}
