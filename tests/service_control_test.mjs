/** Exercise the real worker with BusyBox flock/jsonfilter and a controlled service. */
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { available, launch, stop, until, wait } from './router_test_helpers.mjs';
if (!['flock', 'jsonfilter'].every(available)) {
  console.log('skip service worker (requires OpenWrt flock/jsonfilter; covered by router tests)');
  process.exit(0);
}
const worker = fileURLToPath(new URL('../luci-app-hyper/root/etc/hyper/scripts/service_control.sh', import.meta.url));
const root = mkdtempSync(join(tmpdir(), 'hyper-service-'));
mkdirSync(join(root, 'bin'));
mkdirSync(join(root, 'control'));
const commands = {
  init: `#!/bin/sh
mkdir "$HYPER_TEST_ROOT/active" || { echo overlap >> "$HYPER_TEST_ROOT/calls"; exit 1; }
trap 'rmdir "$HYPER_TEST_ROOT/active" 2>/dev/null || true' EXIT
trap 'exit 143' TERM
echo "$1" >> "$HYPER_TEST_ROOT/calls"
if [ "$1" = stop ]; then
  rm -f "$HYPER_TEST_ROOT/dns-redirect"
  if [ -e "$HYPER_TEST_ROOT/block-cleanup" ]; then
    touch "$HYPER_TEST_ROOT/cleaning"
    while [ ! -e "$HYPER_TEST_ROOT/release-cleanup" ]; do sleep 1; done
  fi
else
  touch "$HYPER_TEST_ROOT/dns-redirect"
fi
if { [ "$1" = start ] && [ -e "$HYPER_TEST_ROOT/block-start" ]; } || { [ "$1" = stop ] && [ -e "$HYPER_TEST_ROOT/block-stop" ]; }; then sleep 30; else sleep 1; fi
[ ! -e "$HYPER_TEST_ROOT/fail-init" ]
`,
  uci: '#!/bin/sh\necho 5334\n',
  curl: '#!/bin/sh\n[ ! -e "$HYPER_TEST_ROOT/fail-ready" ]\n',
};
for (const [name, content] of Object.entries(commands)) {
  const path = join(root, 'bin', name); writeFileSync(path, content); chmodSync(path, 0o755);
}
const env = { ...process.env, HYPER_TEST_ROOT: root, HYPER_INIT: join(root, 'bin/init'),
  HYPER_CONTROL_DIR: join(root, 'control'), HYPER_READY_LIMIT: '2', PATH: `${root}/bin:${process.env.PATH}` };
const workers = [];
function request(id, action, overrides = {}) {
  writeFileSync(join(root, 'control/request.tmp'), JSON.stringify({ id, action, time: Math.floor(Date.now() / 1000) }));
  renameSync(join(root, 'control/request.tmp'), join(root, 'control/request.json'));
  const child = launch('sh', [worker], { env: { ...env, ...overrides } }); workers.push(child); return child;
}
function state() { return JSON.parse(readFileSync(join(root, 'control/state.json'), 'utf8')); }
function calls() { return readFileSync(join(root, 'calls'), 'utf8').trim().split('\n'); }
try {
  request(1, 'start');
  await until(() => existsSync(join(root, 'active')), 'worker did not start', 5000, 10);
  request(2, 'stop');
  workers.push(launch('sh', [worker], { env }));
  for (const child of workers) assert.equal((await wait(child)).code, 0);
  assert.deepEqual(readFileSync(join(root, 'calls'), 'utf8').trim().split('\n'), ['start', 'stop']);
  assert.equal(state().id, 2); assert.equal(state().state, 'done');
  console.log('ok   service worker serializes overlapping requests and skips duplicates');
  writeFileSync(join(root, 'fail-init'), ''); await wait(request(3, 'start'));
  assert.equal(state().state, 'failed'); assert.equal(state().error, 'service_failed');
  assert.deepEqual(calls().slice(-2), ['start', 'stop']);
  assert.equal(existsSync(join(root, 'dns-redirect')), false);
  rmSync(join(root, 'fail-init'));
  writeFileSync(join(root, 'fail-ready'), ''); await wait(request(4, 'start'));
  assert.equal(state().state, 'failed'); assert.equal(state().error, 'not_ready');
  assert.deepEqual(calls().slice(-2), ['start', 'stop']);
  assert.equal(existsSync(join(root, 'dns-redirect')), false);
  rmSync(join(root, 'fail-ready')); await wait(request(5, 'start'));
  assert.equal(state().id, 5); assert.equal(state().state, 'done');
  console.log('ok   service worker reports init/readiness failures and allows retry');
  writeFileSync(join(root, 'block-start'), '');
  request(6, 'start');
  await until(() => existsSync(join(root, 'active')), 'blocking start did not begin');
  const stoppedAt = performance.now();
  await wait(request(7, 'stop'));
  assert(performance.now() - stoppedAt < 6000, 'Stop waited for the obsolete 30-second Start');
  assert.equal(state().id, 7); assert.equal(state().state, 'done');
  assert(!readFileSync(join(root, 'calls'), 'utf8').includes('overlap'));
  console.log('ok   a new Stop interrupts unfinished Start without overlapping init operations');

  await wait(request(8, 'start', { HYPER_INIT_LIMIT: '2' }));
  assert.equal(state().id, 8); assert.equal(state().state, 'failed');
  assert.deepEqual(calls().slice(-2), ['start', 'stop']);
  assert.equal(existsSync(join(root, 'dns-redirect')), false, 'timed-out startup left DNS pointing at an absent core');
  rmSync(join(root, 'block-start'));
  console.log('ok   timed-out startup runs one bounded Stop to unwind partial DNS state');

  for (const file of ['fail-init', 'block-stop']) {
    writeFileSync(join(root, file), '');
    const count = calls().length;
    await wait(request(file === 'fail-init' ? 9 : 10, 'stop', { HYPER_INIT_LIMIT: '2' }));
    assert.equal(state().state, 'failed');
    assert.deepEqual(calls().slice(count), ['stop'], 'failed Stop must not recursively retry cleanup');
    rmSync(join(root, file));
  }
  console.log('ok   Stop failure and timeout remain bounded without recursive cleanup');

  // Replace Stop with Start while the cancellation cleanup is still in progress.
  writeFileSync(join(root, 'block-start'), '');
  writeFileSync(join(root, 'block-cleanup'), '');
  const before = calls().length;
  request(11, 'start');
  await until(() => existsSync(join(root, 'active')), 'last blocking Start did not begin');
  request(12, 'stop');
  await until(() => existsSync(join(root, 'cleaning')), 'cancellation cleanup did not begin');
  rmSync(join(root, 'block-start'));
  const last = request(13, 'start');
  writeFileSync(join(root, 'release-cleanup'), '');
  await wait(last);
  assert.equal(state().id, 13); assert.equal(state().state, 'done');
  assert.deepEqual(calls().slice(before), ['start', 'stop', 'start']);
  assert.equal(existsSync(join(root, 'dns-redirect')), true);
  assert(!calls().includes('overlap'));
  console.log('ok   a new Start during cancellation cleanup survives and runs after Stop completes');
} finally {
  await Promise.all(workers.map(stop));
  rmSync(root, { recursive: true, force: true });
}
