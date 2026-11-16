/** Exercise the installed update worker only in a disposable router container.
 * Stub network/package executables; keep real ucode, UCI, locks, checksums,
 * module loading, scheduling and installer state transitions.
 */
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { command, launch, sleep, stop, until, wait } from './router_test_helpers.mjs';
assert.equal(process.env.HYPER_TEST_CONTAINER, '1', 'run only inside router-test.sh disposable container');
const runtime = '/var/run/hyper-software-update';
const worker = '/etc/hyper/scripts/auto_update.sh';
const backend = '/usr/share/rpcd/ucode/luci.hyper';
const control = '/usr/lib/opkg/info/luci-app-hyper.control';
const apk = existsSync('/usr/bin/apk') && existsSync('/etc/apk');
const manager = apk ? '/usr/bin/apk' : '/bin/opkg';
const arch = apk ? command(manager, ['--print-arch']).trim() : command(manager, ['print-architecture']).trim().split('\n')
  .filter(line => line.startsWith('arch ') && !['all', 'noarch'].includes(line.split(/\s+/)[1])).at(-1).split(/\s+/)[1];
const packageName = apk ? `25.12+_luci-app-hyper-9.9.9-${arch}.apk` : `23.05-24.10_luci-app-hyper_9.9.9_${arch}.ipk`;
const fixture = mkdtempSync(join(tmpdir(), 'hyper-update-worker-'));
const file = name => join(fixture, name);
const backups = new Map(), children = [];
function exists(path) { try { lstatSync(path); return true; } catch { return false; } }
function method(name, args = {}) {
  const source = `const m=loadfile('${backend}')()['luci.hyper']; printf('%J',m.${name}.call({args:${JSON.stringify(args)}}));`;
  return JSON.parse(command('/usr/bin/ucode', ['-S', '-e', source], { env: { ...process.env, HYPER_RPC_TASK: '1' } }));
}
function replace(path, text) {
  mkdirSync(dirname(path), { recursive: true }); rmSync(path, { force: true });
  writeFileSync(path, text); chmodSync(path, 0o755);
}
function configure(check, install) {
  command('uci', ['set', `hyper.update.auto_check=${Number(check)}`]);
  command('uci', ['set', `hyper.update.auto_update=${Number(install)}`]); command('uci', ['commit', 'hyper']);
}
function reset(mode = 'success', check = true, install = false) {
  rmSync(runtime, { recursive: true, force: true });
  for (const name of ['requests', 'installs', 'install.started', 'install.gate', 'check.started', 'check.gate']) rmSync(file(name), { force: true });
  writeFileSync(file('mode'), mode); mkdirSync(dirname(control), { recursive: true });
  writeFileSync(control, 'Package: luci-app-hyper\nVersion: 0.1.0-r1\n'); configure(check, install);
}
function count(name) { return existsSync(file(name)) ? readFileSync(file(name), 'utf8').trimEnd().split('\n').length : 0; }
function run(force = true) { command(worker, force ? ['force'] : []); }
function state() { try { return JSON.parse(readFileSync(join(runtime, 'state.json'), 'utf8')); } catch { return {}; } }
const touch = name => writeFileSync(file(name), '');
try {
  for (const path of ['/usr/bin/curl', manager, '/etc/init.d/rpcd', '/etc/config/hyper', control]) {
    const backup = file(`backup-${backups.size}`);
    if (exists(path)) { cpSync(path, backup, { dereference: false, preserveTimestamps: true, verbatimSymlinks: true }); backups.set(path, backup); }
    else backups.set(path, null);
  }
  if (existsSync(runtime)) cpSync(runtime, file('runtime-backup'), { recursive: true, preserveTimestamps: true, verbatimSymlinks: true });
  const content = Buffer.from('test package data verified by the real checksum path\n');
  writeFileSync(file('package'), content);
  writeFileSync(file('sha256sums'), `${createHash('sha256').update(content).digest('hex')}  ${packageName}\n`);
  writeFileSync(file('release'), JSON.stringify({ tag_name: 'v9.9.9', assets: [
    { name: packageName, browser_download_url: 'https://release.test/package' },
    { name: 'sha256sums', browser_download_url: 'https://release.test/sha256sums' },
  ] }));
  // .mjs files keep ESM explicit even when the replacement executable has no suffix.
  writeFileSync(file('curl.mjs'), `import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { setTimeout as sleep } from 'node:timers/promises';
const root=${JSON.stringify(fixture)}, config={};
for (const line of readFileSync(process.argv[3], 'utf8').split('\\n')) {
  const at=line.indexOf(' = ');
  if (at>=0) try { config[line.slice(0,at)]=JSON.parse(line.slice(at+3)); } catch {}
}
const url=config.url; appendFileSync(root+'/requests',url+'\\n');
const mode=readFileSync(root+'/mode','utf8');
const name=url.includes('/releases/latest')?'release':url.split('/').at(-1);
if(mode==='slow-check'&&name==='release') {writeFileSync(root+'/check.started','');while(!existsSync(root+'/check.gate')) await sleep(20);}
if((mode==='cancel-check'&&name==='release')||(mode==='cancel-download'&&name==='package')) {
  execFileSync('uci',['set','hyper.update.auto_update=0']);execFileSync('uci',['commit','hyper']);
}
const status=mode==='check-failed'?'503':'200';
let data=status==='503'?Buffer.from('{}'):readFileSync(root+'/'+name);
if(mode==='bad-checksum'&&name==='package') data=Buffer.concat([data,Buffer.from('corrupt')]);
writeFileSync(config.output,data);process.stdout.write(status);
`);
  writeFileSync(file('manager.mjs'), `import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { setTimeout as sleep } from 'node:timers/promises';
const root=${JSON.stringify(fixture)}, args=process.argv.slice(2);
if(args.includes('--print-arch')) {console.log(${JSON.stringify(arch)});process.exit(0);}
if(args.includes('print-architecture')) {console.log(${JSON.stringify(`arch all 1\narch ${arch} 10`)});process.exit(0);}
appendFileSync(root+'/installs',args.join(' ')+'\\n');writeFileSync(root+'/install.started','');
while(!existsSync(root+'/install.gate')) await sleep(20);
if(readFileSync(root+'/mode','utf8')==='install-failed') process.exit(1);
writeFileSync(${JSON.stringify(control)},'Package: luci-app-hyper\\nVersion: 9.9.9-r1\\n');
`);
  // Use the current test runtime, which may live under /opt in firmware without node.
  replace('/usr/bin/curl', `#!/bin/sh\nexec '${process.env.HYPER_TEST_NODE || process.execPath}' '${file('curl.mjs')}' "$@"\n`);
  replace(manager, `#!/bin/sh\nexec '${process.env.HYPER_TEST_NODE || process.execPath}' '${file('manager.mjs')}' "$@"\n`);
  replace('/etc/init.d/rpcd', `#!/bin/sh\nprintf 'reload\\n' >> '${file('reloads')}'\n`);

  reset('success', false, false); run(); assert.equal(count('requests'), 0);
  reset(); let checked = method('check_update');
  assert.equal(checked.current, '0.1.0', 'public version hides internal package revision');
  assert(readFileSync(control, 'utf8').includes('Version: 0.1.0-r1'), 'display formatting must not rewrite package metadata');
  assert(checked.installable, 'release assets without a revision must match this router');
  const release = JSON.parse(readFileSync(file('release'), 'utf8'));
  release.assets[0].name = packageName.replace('9.9.9', '9.9.9-r1'); writeFileSync(file('release'), JSON.stringify(release));
  assert(method('check_update').installable, 'older release assets must remain installable');
  release.assets[0].name = packageName; writeFileSync(file('release'), JSON.stringify(release));
  reset(); run(); assert.equal(JSON.parse(readFileSync(join(runtime, 'check.json'), 'utf8')).latest, '9.9.9');
  assert.equal(count('requests'), 1); assert.equal(count('installs'), 0);
  run(false); assert.equal(count('requests'), 1);
  console.log('ok   enabled worker loads real rpcd module; check-only and daily throttle work');

  reset('slow-check'); const child = launch(worker, ['force']); children.push(child);
  await until(() => existsSync(file('check.started')), 'scheduler did not start');
  run(); assert.equal(count('requests'), 1); touch('check.gate'); assert.equal((await wait(child)).code, 0);
  console.log('ok   simultaneous scheduler invocations share one check');

  for (const mode of ['check-failed', 'cancel-check', 'cancel-download', 'bad-checksum']) {
    reset(mode, false, true); run(); assert.equal(count('installs'), 0, mode);
  }
  console.log('ok   failed checks, bad checksums and disabling during check/download never install');

  reset('success', false, true); run();
  await until(() => existsSync(file('install.started')), 'detached installer did not take over lock');
  const old = state(); old.time = Math.floor(Date.now() / 1000) - 3600;
  writeFileSync(join(runtime, 'state.json'), JSON.stringify(old));
  const requests = count('requests');
  assert.equal(method('install_update', { force: true }).error, 'update_in_progress');
  assert.equal(count('requests'), requests); assert.equal(count('installs'), 1);
  assert.equal(method('status').update.state.state, 'installing');
  assert.equal(count('requests'), requests, 'local status must not fetch GitHub');
  touch('install.gate'); await until(() => state().state === 'done', 'installer did not finish');
  assert(!existsSync(join(runtime, 'download')) && !existsSync(join(runtime, 'install.json')));
  assert.equal(method('status').update.state.state, 'done'); assert.equal(method('status').version, '9.9.9');
  assert(readFileSync(control, 'utf8').includes('Version: 9.9.9-r1'), 'installed metadata keeps SDK version');
  assert.equal(count('requests'), requests); assert.equal(method('install_update').error, 'up_to_date', 'lock releases after cleanup');
  console.log('ok   independent automatic install holds its lock past stale timestamps; local status and cleanup work');

  reset('install-failed', false, false); touch('install.gate'); assert(method('install_update').started);
  await until(() => state().state === 'failed', 'package failure was not published');
  assert.equal(count('installs'), 1); assert(!existsSync(join(runtime, 'download')));
  console.log('ok   manual install remains independent of switches; package failure publishes terminal state');

  reset('success', false, false); const download = join(runtime, 'download'); mkdirSync(download, { recursive: true });
  const staged = join(download, packageName); writeFileSync(staged, content);
  writeFileSync(join(runtime, 'install.json'), JSON.stringify({ manager: apk ? 'apk' : 'opkg', files: [staged], version: '9.9.9', automatic: true }));
  const reloads = count('reloads'); command(worker, ['install']);
  assert.equal(state().error, 'automatic_update_disabled'); assert.equal(count('installs'), 0); assert(!existsSync(download));
  assert.equal(count('reloads'), reloads, 'cancelled installation must not reload rpcd');
  console.log('ok   disabling automatic updates before lock handoff cancels installation');
} finally {
  touch('install.gate'); touch('check.gate'); await Promise.all(children.map(stop)); await sleep(200);
  for (const [path, backup] of backups) {
    rmSync(path, { force: true });
    if (backup) cpSync(backup, path, { dereference: false, preserveTimestamps: true, verbatimSymlinks: true });
  }
  rmSync(runtime, { recursive: true, force: true });
  if (existsSync(file('runtime-backup'))) cpSync(file('runtime-backup'), runtime, { recursive: true, preserveTimestamps: true, verbatimSymlinks: true });
  rmSync(fixture, { recursive: true, force: true });
}
