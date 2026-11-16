/** Exercise rule storage as root and read it as the real unprivileged service. */
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, linkSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { command, launch, stop, until, wait } from './router_test_helpers.mjs';

if (!existsSync('/etc/openwrt_release') || process.getuid() !== 0) {
  console.log('skip rule permissions (requires root in the disposable OpenWrt integration container)');
  process.exit(0);
}
// Exercise the installed payload, so package tests also catch missing or stale files.
const scripts = '/etc/hyper/scripts';
const helper = join(scripts, 'rules-storage.sh');
const root = mkdtempSync(join(tmpdir(), 'hyper-rules-'));
const rules = join(root, 'rules');
const gid = Number(command('id', ['-g', 'sing-box']).trim());
const users = Object.fromEntries(['sing-box', 'nobody'].map(user => [user, {
  uid: Number(command('id', ['-u', user]).trim()),
  gid: Number(command('id', ['-g', user]).trim()),
}]));
assert.notEqual(users['sing-box'].uid, 0);
assert.notEqual(users.nobody.uid, 0);
const env = { ...process.env, HYPER_RULES_DIR: rules, HYPER_TEST_ROOT: root };
const quoted = value => `'${value.replaceAll("'", "'\\''")}'`;
const run = (args, options = {}) => command('sh', ['-c', 'umask 077; exec sh "$@"', 'rules-test', helper, ...args], { env, ...options });
const readAs = (user, path) => spawnSync('/bin/cat', [path], { encoding: 'utf8', ...users[user] });
const mode = path => statSync(path).mode & 0o777;
function readable(path, expected) {
  assert.equal(mode(path), 0o640);
  assert.equal(statSync(path).uid, 0);
  assert.equal(statSync(path).gid, gid);
  const read = readAs('sing-box', path);
  assert.equal(read.status, 0, read.error?.message || read.stderr);
  assert.equal(read.stdout, expected);
  assert.notEqual(readAs('nobody', path).status, 0);
}
function noTemps() { assert.equal(readdirSync(rules).filter(name => name.startsWith('.rule.')).length, 0); }
function stub(name, content) {
  const path = join(root, 'bin', name);
  writeFileSync(path, '#!/bin/sh\n' + content);
  chmodSync(path, 0o755);
}

let writer;
try {
  chmodSync(root, 0o755);
  mkdirSync(join(root, 'bin'));
  mkdirSync(rules, { mode: 0o700 });
  const rule = join(rules, 'YouTube.domain.json');
  const initial = '{"version":3,"rules":[{"domain_suffix":["youtube.com"]}]}\n';
  writeFileSync(rule, initial, { mode: 0o600 });
  writeFileSync(join(root, 'session'), 'private session', { mode: 0o600 });
  assert.notEqual(readAs('sing-box', rule).status, 0, 'old root-only rule must reproduce the failure');
  run(['prepare']);
  assert.equal(mode(rules), 0o750);
  assert.equal(statSync(rules).uid, 0);
  assert.equal(statSync(rules).gid, gid);
  readable(rule, initial);
  assert.equal(mode(join(root, 'session')), 0o600);
  assert.equal(statSync(join(root, 'session')).gid, 0);
  assert.notEqual(readAs('sing-box', join(root, 'session')).status, 0);
  console.log('ok   old root-only rules become service-readable without exposing sibling secrets');

  // Invalid existing links must never change the target's secret permissions.
  for (const makeLink of [symlinkSync, linkSync]) {
    const linked = join(rules, 'linked.json');
    makeLink(join(root, 'session'), linked);
    assert.throws(() => run(['prepare']));
    assert.equal(mode(join(root, 'session')), 0o600);
    assert.equal(statSync(join(root, 'session')).gid, 0);
    rmSync(linked);
  }
  assert.throws(() => run(['publish', '../session'], { input: 'bad' }));
  assert.equal(readFileSync(join(root, 'session'), 'utf8'), 'private session');

  const changed = '{"version":3,"rules":[{"domain_suffix":["ytimg.com"]}]}\n';
  // Pause the actual write before it finishes: readers must still see the old file.
  stub('cat', 'touch "$HYPER_TEST_ROOT/writing"\nwhile [ ! -e "$HYPER_TEST_ROOT/release" ]; do sleep 1; done\nexec /bin/cat\n');
  const stubEnv = { ...env, PATH: `${root}/bin:${process.env.PATH}` };
  writer = launch('sh', [helper, 'publish', 'YouTube.domain.json'], { env: stubEnv, stdio: ['pipe', 'inherit', 'inherit'] });
  writer.stdin.end(changed);
  await until(() => existsSync(join(root, 'writing')), 'publisher did not start');
  readable(rule, initial);
  const temporary = readdirSync(rules).find(name => name.startsWith('.rule.'));
  assert.ok(temporary);
  assert.notEqual(readAs('sing-box', join(rules, temporary)).status, 0);
  writeFileSync(join(root, 'release'), '');
  assert.equal((await wait(writer)).code, 0);
  readable(rule, changed);
  rmSync(join(root, 'bin/cat'));
  noTemps();

  for (const operation of ['cat', 'chown', 'chmod', 'mv']) {
    const real = command('sh', ['-c', `command -v ${operation}`]).trim();
    stub(operation, operation === 'cat' ? 'printf partial; exit 71\n' : `case "$*" in *.rule.*) exit 71 ;; esac\nexec ${quoted(real)} "$@"\n`);
    assert.throws(() => run(['publish', 'YouTube.domain.json'], { env: stubEnv, input: 'unpublished' }));
    readable(rule, changed);
    noTemps();
    rmSync(join(root, 'bin', operation));
  }
  console.log('ok   atomic writes preserve the previous readable rule on write/metadata/rename failure');

  // Run the actual synchronization functions with a local catalog instead of HTTP.
  const source = readFileSync(join(scripts, 'sync_subscription.uc'), 'utf8');
  const functions = source.slice(source.indexOf('function publish_rule('), source.indexOf('\nfunction main()'));
  assert.ok(functions.includes('function sync_rules('));
  const harness = join(root, 'sync.uc');
  writeFileSync(harness, `
import { mkstemp, readfile } from 'fs';
import { executeCommand, shellQuote } from 'hyper';
import { normalizeCatalog, parseRuleList, toRuleSetSources } from 'rules';
const RULES_DIR = ${JSON.stringify(rules)};
const RULE_STORAGE = ${JSON.stringify(helper)};
function log(...args) { print(join(' ', args) + '\\n'); }
function same_session() { return true; }
function sync_request() { return { ok: true, data: [{ key: 'YouTube', policy: 'PROXY', value: 'DOMAIN-SUFFIX,youtube.com\\nIP-CIDR,1.2.3.0/24' }] }; }
function read_auto(fn) { return fn(null); }
function httpRequest() { die('unexpected network request'); }
${functions}
const result = sync_rules({ path: '/fixture' }, null);
exit(result.ok ? 0 : 1);
`);
  const sync = extraEnv => command('sh', ['-c', 'umask 077; exec ucode -S -L "$1" "$2"', 'rules-sync-test', scripts, harness], { env: { ...env, ...extraEnv } });
  rmSync(rules, { recursive: true });
  sync();
  assert.equal(mode(rules), 0o750);
  for (const name of ['YouTube.domain.json', 'YouTube.ip.json', 'index.json']) {
    readable(join(rules, name), readFileSync(join(rules, name), 'utf8'));
  }
  const index = join(rules, 'index.json');
  const previousIndex = '[{"key":"old"}]\n';
  run(['publish', 'index.json'], { input: previousIndex });
  run(['publish', 'old.domain.json'], { input: initial });
  const realMv = command('sh', ['-c', 'command -v mv']).trim();
  stub('mv', `case "$*" in */index.json) exit 71 ;; esac\nexec ${quoted(realMv)} "$@"\n`);
  assert.throws(() => sync({ PATH: stubEnv.PATH }));
  readable(index, previousIndex);
  readable(join(rules, 'old.domain.json'), initial);
  noTemps();
  rmSync(join(root, 'bin/mv'));
  sync();
  assert.equal(existsSync(join(rules, 'old.domain.json')), false);
  readable(index, readFileSync(index, 'utf8'));
  console.log('ok   real ucode sync creates rules/index under umask 077 and keeps old index/files on failure');
} finally {
  await stop(writer);
  rmSync(root, { recursive: true, force: true });
}
