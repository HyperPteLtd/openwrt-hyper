import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { setTimeout as sleep } from 'node:timers/promises';

const helper = fileURLToPath(new URL('../luci-app-hyper/root/usr/libexec/hyper/proxy-conflicts', import.meta.url));
const services = ['openclash', 'passwall', 'passwall2', 'shadowsocksr', 'homeproxy', 'nikki', 'mihomo', 'momo', 'v2raya'];
const node = process.env.HYPER_TEST_NODE || process.execPath;
const native = name => spawnSync('sh', ['-c', 'command -v ' + name], { encoding: 'utf8' }).status === 0;
function file(path, content, executable = false) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content);
  if (executable) chmodSync(path, 0o755);
}
function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'hyper-conflicts-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const dir of ['bin', 'init', 'proc', 'run', 'dns-config']) mkdirSync(join(root, dir));
  const write = (path, data) => file(join(root, path), data);
  const json = (path, data) => write(path, JSON.stringify(data));
  const script = (name, lines, useNode = false) => file(join(root, 'bin', name),
    '#!' + (useNode ? node : '/bin/sh') + '\n' + lines.join('\n') + '\n', true);
  json('services', {}); write('nft', ''); write('iptables', '');
  write('settings', 'enabled=1\n');
  // Every plugin starts at boot until its disable removes the link.
  services.forEach(name => write('enabled/' + name, ''));
  script('uci', ['[ ! -e "$TEST_ROOT/cancel" ] && echo 1']);
  script('ubus', ['[ ! -e "$TEST_ROOT/ubus-fail" ] || exit 1', 'cat "$TEST_ROOT/services"']);
  script('nft', ['[ ! -e "$TEST_ROOT/nft-fail" ] || exit 1', 'cat "$TEST_ROOT/nft"']);
  script('iptables-save', ['cat "$TEST_ROOT/iptables"']);
  script('ip6tables-save', ['exit 0']);
  if (!native('jsonfilter')) script('jsonfilter', [
    "const fs = require('node:fs'); const args = process.argv.slice(2);",
    "try { const data = JSON.parse(fs.readFileSync(args[args.indexOf('-i') + 1], 'utf8'));",
    "const path = args[args.indexOf('-e') + 1];",
    "if (path === '@') console.log(JSON.stringify(data));",
    "else if (path.includes('sing-box-c')) console.log(data.homeproxy?.instances?.['sing-box-c']?.running ?? '');",
    "else { const name = path.match(/^@\\[\"([^\"]+)\"\\]/)[1];",
    "for (const instance of Object.values(data[name]?.instances || {})) if (instance.running !== undefined) console.log(instance.running); }",
    "} catch { process.exit(1); }",
  ], true);
  // macOS lacks the BusyBox applet. A detached Node child establishes the same
  // session boundary; Linux routers exercise the production bundled fallback
  // when their BusyBox does not include setsid.
  if (process.platform === 'darwin' && !native('setsid')) script('setsid', [
    "const {spawn} = require('node:child_process');",
    "const child = spawn(process.argv[2], process.argv.slice(3), {detached:true, stdio:'inherit'});",
    "child.on('error', () => process.exit(1)); child.on('exit', code => process.exit(code ?? 1));",
  ], true);
  // hyperd run-session: exec in a new session (setsid, or a detached Node child on macOS).
  if (native('setsid')) script('hyperd', ['shift', 'exec setsid "$@"']);
  else script('hyperd', [
    "const {spawn} = require('node:child_process');",
    "const child = spawn(process.argv[3], process.argv.slice(4), {detached:true, stdio:'inherit'});",
    "child.on('error', () => process.exit(1)); child.on('exit', code => process.exit(code ?? 1));",
  ], true);
  script('clear-service', [
    "const fs = require('node:fs'); const root = process.env.TEST_ROOT, name = process.argv[2];",
    "const data = JSON.parse(fs.readFileSync(root + '/services'));",
    "delete data[name]; if(name === 'openclash') delete data['openclash-watchdog'];",
    "fs.writeFileSync(root + '/services', JSON.stringify(data));",
    "for(const pid of fs.readdirSync(root + '/proc')) { const owner = root + '/proc/' + pid + '/owner';",
    "if(fs.existsSync(owner) && fs.readFileSync(owner,'utf8') === name) fs.rmSync(root + '/proc/' + pid, {recursive:true}); }",
    "fs.writeFileSync(root + '/nft',''); fs.writeFileSync(root + '/iptables','');",
  ], true);
  const install = (name, body = ['clear-service "$name"'], disable = ['rm -f "$TEST_ROOT/enabled/$name"']) => file(join(root, 'init', name), [
    '#!/bin/sh', 'name=' + name,
    'case "$1" in',
    'enabled) [ -e "$TEST_ROOT/enabled/$name" ]; exit ;;',
    'disable) echo "$name:$1" >> "$TEST_ROOT/calls"', ...disable, 'exit 0 ;;',
    'stop) echo "$name:$1" >> "$TEST_ROOT/calls" ;;',
    '*) exit 99 ;;',
    'esac', ...body,
  ].join('\n') + '\n', true);
  services.forEach(name => install(name));
  const env = { ...process.env, TEST_ROOT: root, PATH: join(root, 'bin') + ':' + process.env.PATH,
    HYPER_RUN_SESSION: join(root, 'bin', 'hyperd'), HYPER_CONFLICT_INIT: join(root, 'init'), HYPER_CONFLICT_PROC: join(root, 'proc'),
    HYPER_CONFLICT_DNS_CONFIG: join(root, 'dns-config'),
    HYPER_CONFLICT_TMP: join(root, 'run'), HYPER_CONFLICT_LIMIT: '3' };
  const run = async (extra = {}) => {
    const child = spawn('sh', [helper], { env: { ...env, ...extra } });
    let stderr = ''; child.stderr.on('data', chunk => { stderr += chunk; });
    const timer = setTimeout(() => child.kill('SIGTERM'), 120000);
    const code = await new Promise((resolve, reject) => {
      child.on('error', reject); child.on('exit', resolve);
    }).finally(() => clearTimeout(timer));
    return { code, stderr };
  };
  const recordProcess = (pid, args, owner) => {
    write('proc/' + pid + '/cmdline', args.join('\0') + '\0');
    if (owner) write('proc/' + pid + '/owner', owner);
  };
  const calls = () => existsSync(join(root, 'calls')) ? readFileSync(join(root, 'calls'), 'utf8').trim().split('\n') : [];
  const enabled = name => existsSync(join(root, 'enabled', name));
  return { root, write, json, install, env, run, process: recordProcess, calls, enabled };
}

test('installed but idle proxies, generic cores and server-only HomeProxy stay untouched', async t => {
  const f = fixture(t);
  f.json('services', { homeproxy: { instances: { 'sing-box-s': { running: true }, 'log-cleaner': { running: true } } },
    hyper: { instances: { 'sing-box-c': { running: true } } } });
  f.process(100, ['/usr/libexec/hyper/bin/sing-box', 'run', '--config', '/var/run/hyper/sing-box-c.json']);
  f.process(101, ['/usr/bin/sing-box', 'run', '--config', '/etc/standalone.json']);
  f.process(102, ['/usr/bin/sing-box', 'run', '--config', '/var/run/homeproxy/sing-box-s.json']);
  f.process(103, ['tail', '-f', '/etc/openclash/clash.log']);
  assert.equal((await f.run()).code, 0);
  assert.deepEqual(f.calls(), []);
  assert.ok(services.every(f.enabled), 'an idle plugin lost its boot link');
});

test('all registered running services stop through their init and stay off at boot, their settings untouched', async t => {
  const f = fixture(t);
  f.json('services', Object.fromEntries(services.map(name => [name, { instances: {
    [name === 'homeproxy' ? 'sing-box-c' : 'core']: { running: true },
  } }])));
  const result = await f.run();
  assert.equal(result.code, 0, result.stderr);
  assert.deepEqual(f.calls(), [...services.map(name => name + ':stop'), ...services.map(name => name + ':disable')]);
  assert.equal(readFileSync(join(f.root, 'settings'), 'utf8'), 'enabled=1\n');
  assert.ok(!services.some(f.enabled), 'a stopped plugin still starts at boot');
});

test('legacy unmanaged PassWall process and independent OpenClash watchdog are detected', async t => {
  const f = fixture(t);
  f.json('services', { 'openclash-watchdog': { instances: { watcher: { running: true } } } });
  f.process(123, ['/tmp/etc/passwall/bin/xray', '-c', '/tmp/etc/passwall/TCP.json'], 'passwall');
  f.process(124, ['/tmp/etc/passwall2/bin/sing-box', 'run'], 'passwall2');
  assert.equal((await f.run()).code, 0);
  assert.deepEqual(f.calls(), ['openclash:stop', 'passwall:stop', 'passwall2:stop', 'openclash:disable', 'passwall:disable', 'passwall2:disable']);
});

test('remaining nft ownership is cleaned after a core crash', async t => {
  const f = fixture(t);
  f.write('nft', 'table inet ss_spec {\n chain prerouting { type nat hook prerouting priority -100; }\n}\n');
  assert.equal((await f.run()).code, 0);
  assert.deepEqual(f.calls(), ['shadowsocksr:stop', 'shadowsocksr:disable']);
});

test('legacy iptables jumps identify their owner without confusing PassWall versions', async t => {
  const f = fixture(t);
  f.write('iptables', '-A PREROUTING -j PSW2\n');
  assert.equal((await f.run()).code, 0);
  assert.deepEqual(f.calls(), ['passwall2:stop', 'passwall2:disable']);
});

test('HomeProxy nonzero TUN cleanup is accepted only after its active DNS include is removed', async t => {
  const f = fixture(t);
  const dnsDir = join(f.root, 'dns-active');
  f.write('dns-config/dnsmasq.conf.router', 'conf-dir=' + dnsDir + ',*.conf\n');
  f.write('dns-active/dnsmasq-homeproxy.conf', 'conf-dir=' + dnsDir + '/dnsmasq-homeproxy.d\n');
  // DNS residue alone is still ownership even with no running core or rules.
  f.install('homeproxy', ['clear-service "$name"', 'exit 1']);
  const leftover = await f.run({ HYPER_CONFLICT_LIMIT: '1' });
  assert.equal(leftover.code, 1); assert.match(leftover.stderr, /homeproxy still owns/);
  f.install('homeproxy', ['rm "$TEST_ROOT/dns-active/dnsmasq-homeproxy.conf"', 'clear-service "$name"', 'exit 1']);
  const clean = await f.run();
  assert.equal(clean.code, 0, clean.stderr); assert.match(clean.stderr, /verifying process, DNS and firewall cleanup/);
  assert.deepEqual(f.calls(), ['homeproxy:stop', 'homeproxy:stop', 'homeproxy:disable']);
});

test('failed stop and surviving rules prevent takeover', async t => {
  const f = fixture(t);
  f.json('services', { nikki: { instances: { core: { running: true } } } });
  f.install('nikki', ['exit 7']);
  const failed = await f.run();
  assert.equal(failed.code, 1); assert.match(failed.stderr, /nikki stop failed \(exit 7\)/);
  f.install('nikki', ['exit 0']);
  const leftover = await f.run({ HYPER_CONFLICT_LIMIT: '1' });
  assert.equal(leftover.code, 1); assert.match(leftover.stderr, /nikki still owns/);
});

test('blocked stop has a deadline and its child cannot perform late cleanup', async t => {
  const f = fixture(t);
  f.json('services', { passwall: { instances: { core: { running: true } } } });
  f.install('passwall', ['sleep 5', 'echo unsafe-late-cleanup > "$TEST_ROOT/late"', 'clear-service "$name"']);
  const before = performance.now();
  const result = await f.run({ HYPER_CONFLICT_LIMIT: '1' });
  assert.equal(result.code, 1); assert.match(result.stderr, /Timed out or failed stopping passwall/);
  assert.ok(performance.now() - before < 5000);
  await sleep(5000);
  assert.equal(existsSync(join(f.root, 'late')), false);
});

test('waits for asynchronous cleanup before accepting service exit', async t => {
  const f = fixture(t);
  f.json('services', { momo: { instances: { core: { running: true } } } });
  f.install('momo', ['( sleep 1; clear-service "$name" ) &', 'exit 0']);
  const result = await f.run({ HYPER_CONFLICT_LIMIT: '6' });
  assert.equal(result.code, 0, result.stderr);
  assert.deepEqual(f.calls(), ['momo:stop', 'momo:disable']);
});

test('a newer stop intent prevents handover and leaves later proxies alone', async t => {
  const f = fixture(t);
  f.json('services', { openclash: { instances: { core: { running: true } } },
    passwall: { instances: { core: { running: true } } } });
  f.install('openclash', ['clear-service "$name"', 'touch "$TEST_ROOT/cancel"']);
  const result = await f.run();
  assert.equal(result.code, 1); assert.match(result.stderr, /Start cancelled/);
  // No takeover, so the stopped plugin still starts at boot.
  assert.deepEqual(f.calls(), ['openclash:stop']);
  assert.ok(f.enabled('openclash'));
});

test('a plugin that stays enabled at boot prevents takeover', async t => {
  const f = fixture(t);
  f.json('services', { v2raya: { instances: { core: { running: true } } } });
  f.install('v2raya', undefined, ['exit 1']);
  const result = await f.run();
  assert.equal(result.code, 1); assert.match(result.stderr, /v2raya stays enabled at boot/);
  assert.deepEqual(f.calls(), ['v2raya:stop', 'v2raya:disable']);
});

test('inspection errors and recognized orphan ownership fail without guessing a process to kill', async t => {
  const f = fixture(t);
  f.write('ubus-fail', '');
  assert.equal((await f.run()).code, 1);
  rmSync(join(f.root, 'ubus-fail')); f.write('nft-fail', '');
  assert.equal((await f.run()).code, 1);
  rmSync(join(f.root, 'nft-fail'));
  f.write('nft', 'table inet nikki {\n}\n'); rmSync(join(f.root, 'init/nikki'));
  const result = await f.run();
  assert.equal(result.code, 1); assert.match(result.stderr, /nikki.*no stop script/);
  assert.deepEqual(f.calls(), []);
});
