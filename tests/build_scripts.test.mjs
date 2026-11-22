// Real local Git remotes and shell entry points; external SDK tools are stubs.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as fs from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const script = name => join(ROOT, 'scripts', name);
function temporary(t, name) {
  const dir = fs.realpathSync(fs.mkdtempSync(join(tmpdir(), name)));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}
function write(path, text, executable = false) {
  fs.mkdirSync(dirname(path), { recursive: true });
  fs.writeFileSync(path, text);
  if (executable) fs.chmodSync(path, 0o755);
  return path;
}
const read = path => fs.readFileSync(path, 'utf8');
const shell = (path, body) => write(path, '#!/bin/sh\nset -eu\n' + body, true);
const nodeTool = (path, fn) => write(path, '#!' + process.execPath + '\n(' + fn.toString() + ')();\n', true);
const invoke = (path, args = [], options = {}) => spawnSync('/bin/sh', [path, ...args], { encoding: 'utf8', ...options });
const success = result => assert.equal(result.status, 0, result.stdout + result.stderr);
const failed = result => assert.notEqual(result.status, 0, result.stdout + result.stderr);
function which(name) {
  const result = spawnSync('/bin/sh', ['-c', 'command -v "$1"', 'sh', name], { encoding: 'utf8' });
  success(result);
  return result.stdout.trim();
}

test('release: only stable SemVer tags can enter the package pipeline', () => {
  for (const tag of ['v0.0.0', 'v1.2.3', 'v10.200.300']) success(invoke(script('release/validate-tag.sh'), [tag]));
  for (const args of [[], [''], ['vfoo'], ['1.2.3'], ['v1.2'], ['v1.2.3-rc.1'], ['v1.2.3+build.1'],
    ['v01.2.3'], ['v1.02.3'], ['v1.2.03'], ['v1.2.3\nv4.5.6'], ['v1.2.3', 'extra']]) {
    const result = invoke(script('release/validate-tag.sh'), args);
    failed(result); assert(result.stderr.includes('vX.Y.Z')); assert.equal(result.stdout, '');
  }
});

class ReleaseFixture {
  constructor(t) {
    this.base = temporary(t, 'hyper-release-');
    this.repo = join(this.base, 'repo');
    fs.mkdirSync(this.repo);
    this.env = { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0' };
    this.git('init', '-q', '-b', 'main');
    this.git('config', 'user.name', 'CI test');
    this.git('config', 'user.email', 'ci@example.invalid');
    this.git('commit', '--allow-empty', '-qm', 'initial');
    this.first = this.git('rev-parse', 'HEAD').trim();
    this.remote = join(this.base, 'remote.git');
    this.git('clone', '-q', '--bare', this.repo, this.remote);
    this.git('remote', 'add', 'origin', this.remote);
  }
  git(...args) {
    const result = spawnSync('git', args, { cwd: this.repo, env: this.env, encoding: 'utf8' });
    success(result);
    return result.stdout;
  }
  gate(ref = 'refs/heads/main') {
    const output = write(join(this.base, 'output'), '');
    const result = invoke(script('release/check-target.sh'), [], { cwd: this.repo, env: {
      ...this.env, RELEASE_TAG: 'v1.2.3', GITHUB_REF: ref,
      GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: join(this.base, 'summary'),
    } });
    return { ...result, output: read(output) };
  }
  advance() {
    this.git('commit', '--allow-empty', '-qm', 'newer push');
    this.git('push', '-q', 'origin', 'main');
  }
}

test('release: current main publishes', t => {
  const f = new ReleaseFixture(t), result = f.gate();
  success(result); assert.equal(result.output, 'publish=true\ncommit=' + f.first + '\n');
});
test('release: older main skips without retagging the new head', t => {
  const f = new ReleaseFixture(t);
  f.advance(); f.git('checkout', '-q', f.first);
  const result = f.gate();
  success(result); assert.equal(result.output, 'publish=false\n');
  assert(read(join(f.base, 'summary')).includes('newer run'));
  assert.equal(f.git('ls-remote', '--tags', 'origin'), '');
});
test('release: second guard detects a push during artifact download', t => {
  const f = new ReleaseFixture(t);
  assert.equal(f.gate().output, 'publish=true\ncommit=' + f.first + '\n');
  f.advance(); f.git('checkout', '-q', f.first);
  const result = f.gate();
  success(result); assert.equal(result.output, 'publish=false\n');
});
test('release: same-commit lightweight and annotated tags allow reruns', t => {
  const f = new ReleaseFixture(t);
  for (const annotated of [false, true]) {
    f.git('tag', '-f', ...(annotated ? ['-a', '-m', 'release'] : []), 'v1.2.3');
    f.git('push', '-q', '--force', 'origin', 'refs/tags/v1.2.3');
    const result = f.gate();
    success(result); assert.equal(result.output, 'publish=true\ncommit=' + f.first + '\n');
  }
});
test('release: conflicting tag fails without overwriting', t => {
  const f = new ReleaseFixture(t);
  f.git('tag', 'v1.2.3'); f.git('push', '-q', 'origin', 'refs/tags/v1.2.3'); f.advance();
  const result = f.gate();
  failed(result); assert.equal(result.output, ''); assert(result.stdout.includes('different commit'));
  assert(f.git('ls-remote', '--tags', 'origin').startsWith(f.first));
});
test('release: explicit tag may publish after main advances', t => {
  const f = new ReleaseFixture(t);
  f.git('tag', 'v1.2.3'); f.git('push', '-q', 'origin', 'refs/tags/v1.2.3');
  f.advance(); f.git('checkout', '-q', f.first);
  const result = f.gate('refs/tags/v1.2.3');
  success(result); assert.equal(result.output, 'publish=true\ncommit=' + f.first + '\n');
});
test('release: deleted explicit tag fails instead of recreating it at newer main', t => {
  const f = new ReleaseFixture(t);
  f.git('tag', 'v1.2.3'); f.git('push', '-q', 'origin', 'refs/tags/v1.2.3');
  f.advance(); f.git('checkout', '-q', f.first);
  f.git('push', '-q', 'origin', ':refs/tags/v1.2.3');
  const result = f.gate('refs/tags/v1.2.3');
  failed(result); assert.equal(result.output, ''); assert(result.stdout.includes('no longer exists on origin'));
  assert.equal(f.git('ls-remote', '--tags', 'origin'), '');
});
test('release: remote failures never allow publishing', t => {
  const f = new ReleaseFixture(t);
  f.git('remote', 'set-url', 'origin', join(f.base, 'missing'));
  for (const ref of ['refs/heads/main', 'refs/tags/v1.2.3']) {
    const result = f.gate(ref);
    failed(result); assert.equal(result.output, '');
  }
});

test('release: invalid tag cannot bypass the final publish guard', t => {
  const f = new ReleaseFixture(t), output = write(join(f.base, 'output'), '');
  const result = invoke(script('release/check-target.sh'), [], { cwd: f.repo, env: {
    ...f.env, RELEASE_TAG: 'v1.2.3-rc.1', GITHUB_REF: 'refs/tags/v1.2.3-rc.1', GITHUB_OUTPUT: output,
  } });
  failed(result); assert(result.stderr.includes('vX.Y.Z')); assert.equal(read(output), '');
  assert.equal(f.git('ls-remote', '--tags', 'origin'), '');
});

function publicName(name, family, arch) {
  const result = invoke(script('package-name.sh'), [name, family, arch]);
  success(result);
  return result.stdout.trim();
}
test('package names: IPK/APK versions and CPU architectures', () => {
  const versions = [['0.4.0-r1', '0.4.0'], ['0.4.0-r12', '0.4.0'], ['0.4.0', '0.4.0'],
    ['0.4.0-beta.1-r2', '0.4.0-beta.1'], ['0.4.0-beta.1', '0.4.0-beta.1'], ['1.0.0-1', '1.0.0-1']];
  for (const [version, shown] of versions) {
    for (const arch of ['x86_64', 'aarch64_cortex-a53', 'arm_cortex-a9_vfpv3-d16', 'mipsel_24kc', 'riscv64_generic', 'loongarch64_generic']) {
      assert.equal(publicName('luci-app-hyper_' + version + '_' + arch + '.ipk', '23.05-24.10', arch),
        '23.05-24.10_luci-app-hyper_' + shown + '_' + arch + '.ipk');
      assert.equal(publicName('luci-app-hyper-' + version + '.apk', '25.12+', arch),
        '25.12+_luci-app-hyper-' + shown + '-' + arch + '.apk');
    }
  }
});
test('package names: only the terminal version revision is removed', () => {
  assert.equal(publicName('/tmp/sdk/luci-app-hyper_0.4.0-r1_arm_r1.ipk', '23.05-24.10', 'arm_r1'),
    '23.05-24.10_luci-app-hyper_0.4.0_arm_r1.ipk');
});
test('package names: reject mismatched architectures, families and filenames', () => {
  for (const args of [
    ['luci-app-hyper_0.4.0-r1_x86_64.ipk', '23.05-24.10', 'aarch64_generic'],
    ['luci-app-hyper_0.4.0-r1_x86_64.ipk', '25.12+', 'x86_64'],
    ['luci-app-hyper-0.4.0-r1.apk', '23.05-24.10', 'x86_64'],
    ['other-package-0.4.0-r1.apk', '25.12+', 'x86_64'],
    ['luci-app-hyper-0.4.0-r1.apk', '25.12+', '../x86_64'],
    ['luci-app-hyper--r1.apk', '25.12+', 'x86_64'],
  ]) {
    const result = invoke(script('package-name.sh'), args);
    failed(result); assert.equal(result.stdout, '');
  }
});

function sdkMakeStub() {
  const fs = require('node:fs');
  const args = process.argv.slice(2);
  const keys = ['TOPDIR', 'SDK', 'STAGING_DIR_HOST', 'PATH', 'LC_ALL', 'HOST_OS', 'HOST_ARCH', 'GNU_HOST_NAME'];
  fs.appendFileSync(process.env.MAKE_LOG, JSON.stringify({ args, env: Object.fromEntries(keys.map(key => [key, process.env[key]])) }) + '\n');
  for (const arg of args) if (arg.startsWith('/') && arg.endsWith('.pem')) fs.writeFileSync(arg, 'official-target-fixture');
}
function sdkTarStub() {
  if (process.argv.includes('-tvf')) console.log('-rw-r--r-- ' + (process.env.PROBE_OWNER || '0/0') + ' 0 2026-01-01 owner');
}
class SdkFixture {
  constructor(t) {
    this.base = temporary(t, 'hyper-sdk-package-');
    this.sdk = join(this.base, 'sdk');
    this.package = join(this.sdk, 'package/feeds/hyper/luci-app-hyper');
    this.host = join(this.sdk, 'staging_dir/host/bin');
    this.log = join(this.base, 'make.jsonl');
    write(join(this.sdk, 'rules.mk'), '# SDK fixture\n');
    write(join(this.sdk, 'feeds/luci/luci.mk'), '# LuCI fixture\n');
    write(join(this.sdk, 'tmp/.packageinfo'), 'Package: luci-app-hyper\n');
    write(join(this.sdk, 'tmp/.packageauxvars'), '');
    write(join(this.sdk, '.config'), 'CONFIG_HAVE_DOT_CONFIG=y\nCONFIG_PACKAGE_luci-app-hyper=m\n');
    write(join(this.package, 'Makefile'), read(join(ROOT, 'luci-app-hyper/Makefile')));
    write(join(this.package, 'htdocs/luci-static/hyper/index.html'), 'app');
    for (const name of ['mihomo', 'hyperd']) write(join(this.package, 'root/usr/libexec/hyper/bin', name), 'prebuilt', true);
    shell(join(this.sdk, 'scripts/config.guess'), 'echo x86_64-pc-linux-gnu\n');
    shell(join(this.host, 'fakeroot'), 'exec "$@"\n');
    nodeTool(join(this.host, 'tar'), sdkTarStub);
    nodeTool(join(this.host, 'make'), sdkMakeStub);
    for (const name of ['openssl', 'apk']) shell(join(this.host, name), 'exit 0\n');
    const fakebin = join(this.base, 'bin');
    shell(join(fakebin, 'id'), 'echo 1000\n');
    this.env = { ...process.env, PATH: fakebin + ':' + process.env.PATH, MAKE_LOG: this.log, SOURCE_DATE_EPOCH: '1794824029' };
  }
  run() { return invoke(script('sdk-package.sh'), [this.sdk], { env: this.env }); }
  calls() { return read(this.log).trim().split('\n').map(line => JSON.parse(line)); }
}

test('SDK package: uses SDK tools and preserves both source epochs', t => {
  const f = new SdkFixture(t);
  success(f.run());
  const calls = f.calls();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].args.at(-1), 'clean');
  assert.deepEqual(calls[1].args.slice(-2), ['compile', 'V=s']);
  for (const { args, env } of calls) {
    assert.deepEqual(args.slice(0, 2), ['-C', f.package]);
    assert(args.includes('SOURCE_DATE_EPOCH=1794824029'));
    assert(args.includes('PKG_SOURCE_DATE_EPOCH=1794824029'));
    assert.equal(env.TOPDIR, f.sdk); assert.equal(env.SDK, '1');
    assert.equal(env.STAGING_DIR_HOST, dirname(f.host));
    assert(env.PATH.startsWith(f.host + ':')); assert.equal(env.LC_ALL, 'C');
    assert.equal(env.GNU_HOST_NAME, 'x86_64-pc-linux-gnu');
  }
});
test('SDK package: new source, hooks and build dependencies fail before make', t => {
  const f = new SdkFixture(t);
  write(join(f.package, 'src/Makefile'), 'all:\n\tcc main.c\n');
  let result = f.run();
  failed(result); assert(result.stderr.includes('unsupported package input src')); assert(!fs.existsSync(f.log));
  fs.rmSync(join(f.package, 'src'), { recursive: true });
  const makefile = read(join(f.package, 'Makefile'));
  for (const added of ['\ndefine Build/Compile\n\t$(MAKE) -C src\nendef\n', '\nPKG_BUILD_DEPENDS:=new-tool/host\n']) {
    write(join(f.package, 'Makefile'), makefile + added);
    result = f.run();
    failed(result); assert(result.stderr.includes('unsupported package Makefile')); assert(!fs.existsSync(f.log));
  }
});
test('SDK package: ownership probe failures stop before build and clean up', t => {
  const f = new SdkFixture(t); f.env.PROBE_OWNER = '1000/1000';
  const result = f.run();
  failed(result); assert(result.stderr.includes('could not produce root-owned files')); assert(!fs.existsSync(f.log));
  assert.deepEqual(fs.readdirSync(join(f.sdk, 'tmp')).filter(name => name.startsWith('hyper-package.')), []);
});
test('SDK package: APK uses official key targets before packaging', t => {
  const f = new SdkFixture(t);
  fs.appendFileSync(join(f.sdk, '.config'), 'CONFIG_USE_APK=y\n');
  success(f.run());
  const calls = f.calls();
  assert.equal(calls.length, 3);
  assert.deepEqual(calls[0].args.slice(0, 2), ['-C', f.sdk]);
  assert.deepEqual(calls[0].args.slice(-2), [join(f.sdk, 'private-key.pem'), join(f.sdk, 'public-key.pem')]);
  assert.equal(calls[1].args.at(-1), 'clean'); assert.deepEqual(calls[2].args.slice(-2), ['compile', 'V=s']);
});

class EntryFixture {
  constructor(t) {
    this.base = temporary(t, 'hyper-sdk-entry-');
    this.sdk = join(this.base, 'sdk'); this.feed = join(this.base, 'feed');
    this.out = join(this.base, 'artifacts'); this.tools = join(this.base, 'tools');
    this.package = join(this.feed, 'luci-app-hyper');
    write(join(this.package, 'htdocs/luci-static/hyper/index.html'), 'built web app');
    write(join(this.package, 'Makefile'), 'PKG_VERSION:=0.4.1\nLUCI_DEPENDS:=+curl +firewall4\n');
    for (const name of ['mihomo', 'hyperd']) shell(join(this.package, 'root/usr/libexec/hyper/bin', name), 'exit 0\n');
    write(join(this.sdk, 'Makefile'), '# SDK fixture\n');
    write(join(this.sdk, '.config'), 'CONFIG_TARGET_TEST=y\n');
    shell(join(this.sdk, 'scripts/feeds'), 'printf "%s\\n" "$*" >> feed-operations\n');
    shell(join(this.tools, 'id'), 'echo 1000\n');
    shell(join(this.tools, 'make'), ': > configured\n');
    shell(join(this.feed, 'scripts/sdk-package.sh'), 'test -f "$1/configured"\ntest "$PWD" = "$1"\nmkdir -p "$1/bin/packages/test/hyper"\nprintf "%s" "$SOURCE_DATE_EPOCH" > "$1/bin/packages/test/hyper/luci-app-hyper-test.ipk"\n');
    for (const name of ['mkdir', 'awk', 'cat', 'cp', 'sh', 'rm', 'sleep']) fs.symlinkSync(which(name), join(this.tools, name));
    // GNU timeout bounds each feed update in the SDK image; developer machines may lack it.
    try { fs.symlinkSync(which('timeout'), join(this.tools, 'timeout')); } catch { /* runs unbounded */ }
    this.env = { ...process.env, PATH: this.tools, FEED_DIR: this.feed, ARTIFACTS_DIR: this.out, SOURCE_DATE_EPOCH: '1794826729', FEEDS_RETRY_PAUSE: '0' };
    delete this.env.SDK_DIR;
  }
  feeds(base) {
    write(join(this.sdk, 'feeds.conf.default'), [
      base,
      'src-git packages https://git.openwrt.org/feed/packages.git^packages-sha',
      'src-git luci https://git.openwrt.org/project/luci.git^luci-sha',
      'src-git routing https://git.openwrt.org/feed/routing.git^routing-sha',
      'src-git telephony https://git.openwrt.org/feed/telephony.git^telephony-sha',
      'src-git video https://github.com/openwrt/video.git^video-sha', '',
    ].join('\n'));
  }
  run() { return invoke(script('ci-sdk-build.sh'), [], { cwd: this.sdk, env: this.env }); }
}

test('SDK entry: actual 24/25 feed formats preserve options, revisions and metadata', t => {
  const f = new EntryFixture(t);
  for (const base of [
    'src-git-full base https://git.openwrt.org/openwrt/openwrt.git;v24.10.8',
    'src-git --root=package base https://git.openwrt.org/openwrt/openwrt.git^base-sha',
  ]) {
    f.feeds(base);
    const makefile = fs.readFileSync(join(f.package, 'Makefile'));
    success(f.run());
    assert.deepEqual(read(join(f.sdk, 'feeds.conf')).trim().split('\n'), [
      base.replace('https://git.openwrt.org/openwrt/', 'https://github.com/openwrt/'),
      'src-git packages https://github.com/openwrt/packages.git^packages-sha',
      'src-git luci https://github.com/openwrt/luci.git^luci-sha',
      'src-link hyper ' + f.feed,
    ]);
    const operations = read(join(f.sdk, 'feed-operations'));
    assert(operations.includes('update base packages luci hyper')); assert(!operations.includes('update -a'));
    assert(operations.includes('install -p hyper luci-app-hyper'));
    assert.deepEqual(fs.readFileSync(join(f.package, 'Makefile')), makefile);
    assert(read(join(f.sdk, '.config')).includes('CONFIG_PACKAGE_luci-app-hyper=m'));
    assert.equal(read(join(f.out, 'bin/packages/test/hyper/luci-app-hyper-test.ipk')), f.env.SOURCE_DATE_EPOCH);
  }
});
test('SDK entry: a failed feed update starts afresh, a third failure stops the build', t => {
  const f = new EntryFixture(t);
  f.feeds('src-git-full base https://git.openwrt.org/openwrt/openwrt.git;v24.10.8');
  // The first update leaves a partial clone behind and fails.
  shell(join(f.sdk, 'scripts/feeds'), 'printf "%s\\n" "$*" >> feed-operations\ncase "$1" in update) if [ ! -e failed-once ]; then : > failed-once; mkdir -p feeds/base/.git; exit 1; fi; test ! -e feeds/base ;; esac\n');
  success(f.run());
  assert.deepEqual(read(join(f.sdk, 'feed-operations')).trim().split('\n'), [
    'update base packages luci hyper', 'update base packages luci hyper', 'install -p hyper luci-app-hyper',
  ]);

  const g = new EntryFixture(t);
  g.feeds('src-git-full base https://git.openwrt.org/openwrt/openwrt.git;v24.10.8');
  shell(join(g.sdk, 'scripts/feeds'), 'printf "%s\\n" "$*" >> feed-operations\nexit 1\n');
  const result = g.run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /feeds unreachable/);
  assert.equal(read(join(g.sdk, 'feed-operations')).trim().split('\n').length, 3);
  assert(!fs.existsSync(join(g.sdk, 'configured')), 'built without feeds');
});

test('SDK entry: missing required feed fails before build', t => {
  const f = new EntryFixture(t);
  f.feeds('# base feed missing');
  const result = f.run();
  failed(result); assert(result.stderr.includes('exactly one base, packages and luci feed'));
  assert(!fs.existsSync(join(f.sdk, 'feed-operations'))); assert(!fs.existsSync(join(f.sdk, 'configured')));
});
test('SDK entry: commit epoch is required and numeric', t => {
  const f = new EntryFixture(t);
  for (const epoch of [undefined, '', 'not-a-timestamp']) {
    if (epoch === undefined) delete f.env.SOURCE_DATE_EPOCH; else f.env.SOURCE_DATE_EPOCH = epoch;
    const result = f.run();
    failed(result); assert(result.stderr.includes('SOURCE_DATE_EPOCH')); assert(!fs.existsSync(f.out));
  }
});
test('SDK entry: unstaged programs fail before feed operations', t => {
  const f = new EntryFixture(t);
  for (const name of ['mihomo', 'hyperd']) {
    const binary = join(f.package, 'root/usr/libexec/hyper/bin', name);
    fs.chmodSync(binary, 0o644);
    const result = f.run();
    failed(result); assert(result.stderr.includes('Stage an executable ' + name));
    assert(!fs.existsSync(join(f.sdk, 'feed-operations'))); assert(!fs.existsSync(f.out));
    fs.chmodSync(binary, 0o755);
  }
});
