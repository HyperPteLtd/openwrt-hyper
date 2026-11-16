// Offline staging regression: real shell/archive handling, stubbed Go/Docker/network.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as fs from 'node:fs';
import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const LOCK = fs.readFileSync(join(ROOT, 'scripts/core.lock'), 'utf8');
const COMMIT = LOCK.match(/^LIBBOX_COMMIT=(.+)$/m)[1];
const SOURCE_ROOT = 'libbox-' + COMMIT;
const ARCHIVE_NAME = SOURCE_ROOT + '.tar.gz';
const TAGS = 'with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0';
const ARCHITECTURES = [
  ['x86_64', 'amd64', 'amd64', '', '', ''],
  ['aarch64_cortex-a53', 'arm64', 'arm64', '', '', ''],
  ['arm_arm1176jzf-s_vfp', 'armv6', 'arm', '6', '', ''],
  ['arm_arm926ej-s', 'armv5', 'arm', '5', '', ''],
  ['mipsel_24kc', 'mipsle-softfloat', 'mipsle', '', 'softfloat', ''],
  ['mips_24kc', 'mips-softfloat', 'mips', '', 'softfloat', ''],
  ['i386_pentium4', '386-softfloat', '386', '', '', 'softfloat'],
  ['riscv64_generic', 'riscv64', 'riscv64', '', '', ''],
  ['loongarch64_generic', 'loong64', 'loong64', '', '', ''],
  ...['cortex-a5_vfpv4', 'cortex-a7_vfpv4', 'cortex-a7_neon-vfpv4', 'cortex-a8_vfpv3',
    'cortex-a9_neon', 'cortex-a9_vfpv3-d16', 'cortex-a15_neon-vfpv4'].map(arch => ['arm_' + arch, 'armv7', 'arm', '7', '', '']),
];

// Small raw ustar fixtures preserve deliberately unsafe names/links; tar CLI
// creation would normalize them before the real staging script could reject them.
function archive(members) {
  const chunks = [];
  for (const { name, content = '', type = '0', link = '' } of members) {
    const bytes = Buffer.from(content);
    const header = Buffer.alloc(512);
    assert(Buffer.byteLength(name) <= 100);
    header.write(name, 0, 100);
    const octal = (offset, width, value) => header.write(value.toString(8).padStart(width - 1, '0') + '\0', offset, width);
    octal(100, 8, 0o644); octal(108, 8, 0); octal(116, 8, 0);
    octal(124, 12, bytes.length); octal(136, 12, 0);
    header.fill(32, 148, 156); header.write(type, 156, 1); header.write(link, 157, 100);
    header.write('ustar\0', 257, 6); header.write('00', 263, 2);
    const checksum = header.reduce((sum, byte) => sum + byte, 0);
    header.write(checksum.toString(8).padStart(6, '0') + '\0 ', 148, 8);
    chunks.push(header, bytes, Buffer.alloc((512 - bytes.length % 512) % 512));
  }
  chunks.push(Buffer.alloc(1024));
  return gzipSync(Buffer.concat(chunks));
}

function goStub() {
  const fs = require('node:fs'), path = require('node:path');
  const args = process.argv.slice(2);
  const keys = ['CGO_ENABLED', 'GOOS', 'GOARCH', 'GOARM', 'GOMIPS', 'GO386', 'GOTOOLCHAIN', 'GOWORK', 'GOFLAGS', 'GOEXPERIMENT', 'GOMODCACHE', 'GOCACHE'];
  fs.appendFileSync(process.env.GO_LOG, JSON.stringify({ args, env: Object.fromEntries(keys.map(key => [key, process.env[key]])) }) + '\n');
  const output = args[args.indexOf('-o') + 1];
  if (process.env.BUILD_FAIL === path.basename(output)) process.exit(19);
  fs.writeFileSync(output, 'built ' + path.basename(output));
}

function curlStub() {
  const fs = require('node:fs');
  const args = process.argv.slice(2);
  fs.writeFileSync(process.env.CURL_LOG, JSON.stringify(args));
  fs.copyFileSync(process.env.DOWNLOAD_ARCHIVE, args[args.indexOf('-o') + 1]);
}

function dockerStub() {
  const fs = require('node:fs'), path = require('node:path'), os = require('node:os');
  const { spawnSync } = require('node:child_process');
  const args = process.argv.slice(2);
  fs.writeFileSync(process.env.DOCKER_LOG, JSON.stringify(args));
  const env = { ...process.env };
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '-e' && args[i + 1].includes('=')) {
      const entry = args[i + 1], split = entry.indexOf('=');
      env[entry.slice(0, split)] = entry.slice(split + 1);
    }
  }
  env.PATH = env.CONTAINER_GO + path.delimiter + env.PATH;
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'hyper-docker-stub-'));
  try {
    // Only this test's generated, previously validated Docker context is extracted.
    const unpacked = spawnSync('tar', ['-xf', '-', '-C', temp], { env, input: fs.readFileSync(0) });
    if (unpacked.status !== 0) { process.stderr.write(unpacked.stderr); process.exitCode = unpacked.status || 1; return; }
    const built = spawnSync('/bin/sh', [path.join(temp, 'build.sh')], { env, stdio: ['ignore', 2, 2] });
    if (built.status !== 0) { process.exitCode = built.status || 1; return; }
    const packed = spawnSync('tar', ['-cf', '-', '-C', path.join(temp, 'bin'), 'sing-box', 'hyper-crypt'], { env });
    if (packed.status !== 0) { process.stderr.write(packed.stderr); process.exitCode = packed.status || 1; return; }
    process.stdout.write(packed.stdout);
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
}

function which(name) {
  const result = spawnSync('/bin/sh', ['-c', 'command -v "$1"', 'sh', name], { encoding: 'utf8' });
  return result.status === 0 ? result.stdout.trim() : null;
}

function pathsWithin(base) {
  const result = [];
  function walk(dir) {
    for (const item of fs.readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, item.name);
      result.push(relative(base, path));
      if (item.isDirectory()) walk(path);
    }
  }
  walk(base);
  return result.sort();
}

class Fixture {
  constructor(t) {
    this.base = fs.mkdtempSync(join(tmpdir(), 'hyper-stage-test-'));
    t.after(() => fs.rmSync(this.base, { recursive: true, force: true }));
    this.repo = join(this.base, 'repo with spaces');
    this.bin = join(this.base, 'tools');
    this.cache = join(this.base, 'cache');
    this.out = join(this.base, 'output');
    this.scratch = join(this.base, 'scratch');
    for (const path of [join(this.repo, 'scripts'), join(this.repo, 'hyper-crypt'), this.bin, this.cache, this.out, this.scratch]) fs.mkdirSync(path, { recursive: true });
    fs.copyFileSync(join(ROOT, 'scripts/stage-binaries.sh'), join(this.repo, 'scripts/stage-binaries.sh'));
    fs.writeFileSync(join(this.repo, 'hyper-crypt/go.mod'), 'module test/hyper-crypt\n\ngo 1.25\n');
    // A closed PATH prevents a real compiler, Docker or network client running.
    for (const name of ['sh', 'dirname', 'mkdir', 'mktemp', 'rm', 'cut', 'mv', 'sed', 'cp', 'chmod', 'ls', 'tar', 'cat', 'awk', 'gzip']) {
      const executable = which(name);
      assert(executable, 'required fixture tool: ' + name);
      fs.symlinkSync(executable, join(this.bin, name));
    }
    const checksum = which('sha256sum') ? 'sha256sum' : 'shasum';
    fs.symlinkSync(which(checksum), join(this.bin, checksum));
    this.env = { ...process.env, PATH: this.bin, OUT_DIR: this.out, CACHE_DIR: this.cache, TMPDIR: this.scratch,
      GO_LOG: join(this.base, 'go.jsonl'), DOCKER_LOG: join(this.base, 'docker.json'), CURL_LOG: join(this.base, 'curl.json') };
    for (const key of ['GOMODCACHE', 'GOCACHE', 'GO_IMAGE', 'BUILD_FAIL']) delete this.env[key];
    this.tool('go', goStub);
    this.tool('curl', curlStub);
    this.source();
  }

  tool(name, fn, dir = this.bin) {
    const path = join(dir, name);
    fs.writeFileSync(path, '#!' + process.execPath + '\n(' + fn.toString() + ')();\n', { mode: 0o755 });
  }

  source({ sdk = '0.1.3', core = 'v1.15.0-alpha.13', extra } = {}) {
    const members = [
      { name: SOURCE_ROOT + '/internal/singbox/runtime.go', content: 'const sdkVersion = "' + sdk + '"\nconst pinnedVersion = "' + core + '"\n' },
      { name: SOURCE_ROOT + '/scripts/build-linux-cli.sh', content: '#!/bin/sh\nexit 99\n' },
    ];
    if (extra) members.push(extra);
    const bytes = archive(members);
    fs.writeFileSync(join(this.cache, ARCHIVE_NAME), bytes);
    const digest = createHash('sha256').update(bytes).digest('hex');
    fs.writeFileSync(join(this.repo, 'scripts/core.lock'), LOCK.replace(/^LIBBOX_SOURCE_SHA256=.+$/m, 'LIBBOX_SOURCE_SHA256=' + digest));
  }

  run(arch = 'x86_64', ok = true) {
    const result = spawnSync('/bin/sh', [join(this.repo, 'scripts/stage-binaries.sh'), arch], { env: this.env, cwd: this.repo, encoding: 'utf8' });
    const output = result.stdout + result.stderr;
    if (ok) {
      assert.equal(result.status, 0, output);
      assert.equal(fs.readFileSync(join(this.out, 'sing-box'), 'utf8'), 'built sing-box');
      assert.equal(fs.readFileSync(join(this.out, 'hyper-crypt'), 'utf8'), 'built hyper-crypt');
      assert.equal(fs.statSync(join(this.out, 'sing-box')).mode & 0o777, 0o755);
    } else assert.notEqual(result.status, 0, output);
    assert.deepEqual(fs.readdirSync(this.scratch), [], 'staging directories must be cleaned on success/failure');
    return result;
  }

  builds() {
    const path = join(this.base, 'go.jsonl');
    return fs.existsSync(path) ? fs.readFileSync(path, 'utf8').trim().split('\n').map(line => JSON.parse(line)) : [];
  }
}

function assertBuildFlags(builds) {
  assert.equal(builds.length, 2);
  for (const { args, env } of builds) {
    assert.equal(args[0], 'build');
    for (const flag of ['-mod=readonly', '-trimpath', '-buildvcs=false']) assert(args.includes(flag));
    assert(args[args.indexOf('-ldflags') + 1].includes('-s -w -buildid='));
    assert.equal(env.CGO_ENABLED, '0'); assert.equal(env.GOOS, 'linux');
    assert.equal(env.GOTOOLCHAIN, 'go1.25.5'); assert.equal(env.GOWORK, 'off');
    assert.equal(env.GOFLAGS, ''); assert.equal(env.GOEXPERIMENT, '');
  }
  const core = builds[0].args;
  assert.equal(core.at(-1), './cmd/sing-box');
  assert.equal(core[core.indexOf('-tags') + 1], TAGS);
  const linker = core[core.indexOf('-ldflags') + 1];
  for (const flag of ['-checklinkname=0', 'github.com/sagernet/sing-box/constant.Version=1.15.0-alpha.13',
    'main.libboxRevision=' + COMMIT, 'internal/singbox.buildTags=' + TAGS,
    'runtime.godebugDefault=multipathtcp=0,tlssha1=1']) assert(linker.includes(flag), flag);
}

test('architectures and shared static flags', t => {
  const f = new Fixture(t);
  for (const [arch, , ...expected] of ARCHITECTURES) {
    f.run(arch);
    const builds = f.builds().slice(-2); assertBuildFlags(builds);
    for (const { env } of builds) {
      assert.deepEqual(['GOARCH', 'GOARM', 'GOMIPS', 'GO386'].map(key => env[key]), expected, arch);
      assert.equal(env.GOMODCACHE, undefined); assert.equal(env.GOCACHE, undefined);
    }
  }
});

test('target IDs need no build tools or filesystem writes', t => {
  const f = new Fixture(t), absent = join(f.base, 'not-created');
  fs.unlinkSync(join(f.repo, 'scripts/core.lock'));
  Object.assign(f.env, { PATH: join(absent, 'tools'), OUT_DIR: join(absent, 'output'), CACHE_DIR: join(absent, 'cache'), TMPDIR: join(absent, 'tmp') });
  const before = pathsWithin(f.base);
  for (const [arch, target] of ARCHITECTURES) {
    const result = spawnSync('/bin/sh', [join(f.repo, 'scripts/stage-binaries.sh'), '--target-id', arch], { env: f.env, cwd: f.repo, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr); assert.equal(result.stdout, target + '\n'); assert.equal(result.stderr, '');
  }
  assert.deepEqual(pathsWithin(f.base), before);
});

test('target ID rejects unknown or missing architecture', t => {
  const f = new Fixture(t); f.env.PATH = join(f.base, 'no-tools');
  for (const args of [['unsupported_cpu'], []]) {
    const result = spawnSync('/bin/sh', [join(f.repo, 'scripts/stage-binaries.sh'), '--target-id', ...args], { env: f.env, cwd: f.repo, encoding: 'utf8' });
    assert.notEqual(result.status, 0); assert.equal(result.stdout, '');
    assert(result.stderr.includes(args.length ? 'no libbox build' : 'usage:'));
  }
  assert.deepEqual(f.builds(), []); assert.deepEqual(fs.readdirSync(f.out), []); assert.deepEqual(fs.readdirSync(f.scratch), []);
  assert(!fs.existsSync(f.env.CURL_LOG));
});

test('cache overrides and toolchain cannot drift', t => {
  const f = new Fixture(t);
  Object.assign(f.env, { GOMODCACHE: join(f.base, 'shared-mod'), GOCACHE: join(f.base, 'shared-build'),
    GOTOOLCHAIN: 'auto', GOFLAGS: '-tags=with_gvisor', GOARM: '6', GOMIPS: 'hardfloat', GO386: 'sse2', GOEXPERIMENT: 'arenas' });
  f.run(); assertBuildFlags(f.builds());
  for (const { env } of f.builds()) {
    assert.equal(env.GOMODCACHE, f.env.GOMODCACHE); assert.equal(env.GOCACHE, f.env.GOCACHE);
    assert.deepEqual(['GOARM', 'GOMIPS', 'GO386'].map(key => env[key]), ['', '', '']);
  }
});

test('verified download uses commit URL', t => {
  const f = new Fixture(t), download = join(f.base, 'download.tar.gz');
  fs.renameSync(join(f.cache, ARCHIVE_NAME), download); f.env.DOWNLOAD_ARCHIVE = download;
  f.run();
  const args = JSON.parse(fs.readFileSync(f.env.CURL_LOG, 'utf8'));
  assert.equal(args.at(-1), 'https://codeload.github.com/HyperPteLtd/libbox/tar.gz/' + COMMIT);
  assert.deepEqual(fs.readFileSync(join(f.cache, ARCHIVE_NAME)), fs.readFileSync(download));
});

test('relative output and cache paths', t => {
  const f = new Fixture(t); Object.assign(f.env, { OUT_DIR: '../output', CACHE_DIR: '../cache' });
  f.run(); assert(!fs.existsSync(f.env.CURL_LOG));
});

test('bad cached archive is rejected before extraction or build', t => {
  const f = new Fixture(t); fs.writeFileSync(join(f.cache, ARCHIVE_NAME), 'untrusted archive');
  assert(f.run('x86_64', false).stderr.includes('checksum mismatch'));
  assert.deepEqual(f.builds(), []); assert(!fs.existsSync(f.env.CURL_LOG));
});

test('bad download is not cached', t => {
  const f = new Fixture(t), download = join(f.base, 'bad-download');
  fs.unlinkSync(join(f.cache, ARCHIVE_NAME)); fs.writeFileSync(download, 'untrusted download'); f.env.DOWNLOAD_ARCHIVE = download;
  assert(f.run('x86_64', false).stderr.includes('checksum mismatch'));
  assert(!fs.existsSync(join(f.cache, ARCHIVE_NAME))); assert.deepEqual(f.builds(), []);
});

test('unsafe paths and links rejected even with matching checksum', t => {
  const f = new Fixture(t);
  for (const [name, type] of [[SOURCE_ROOT + '/../escape', '0'], ['/escape', '0'], [SOURCE_ROOT + '/link', '2'], [SOURCE_ROOT + '/hard', '1']]) {
    f.source({ extra: { name, type, content: type === '0' ? 'x' : '', link: type === '0' ? '' : '../../escape' } });
    assert(f.run('x86_64', false).stderr.includes('unsafe source archive member'), name);
    assert.deepEqual(f.builds(), []);
  }
});

test('source version guard', t => {
  const f = new Fixture(t);
  for (const [sdk, core] of [['0.1.4', 'v1.15.0-alpha.13'], ['0.1.3', '']]) {
    f.source({ sdk, core });
    assert(f.run('x86_64', false).stderr.includes('source version mismatch')); assert.deepEqual(f.builds(), []);
  }
});

test('unsupported architecture', t => {
  const f = new Fixture(t);
  assert(f.run('unsupported_cpu', false).stderr.includes('no libbox build')); assert.deepEqual(f.builds(), []);
});

test('missing compiler never falls back to a prebuilt core', t => {
  const f = new Fixture(t); fs.unlinkSync(join(f.bin, 'go'));
  assert(f.run('x86_64', false).stderr.includes('need Go or Docker'));
  assert.deepEqual(fs.readdirSync(f.out), []); assert(!fs.existsSync(f.env.CURL_LOG));
});

test('failed build does not replace previous binaries', t => {
  const f = new Fixture(t);
  for (const name of ['sing-box', 'hyper-crypt']) fs.writeFileSync(join(f.out, name), 'previous build');
  f.env.BUILD_FAIL = 'hyper-crypt';
  assert.equal(f.run('x86_64', false).status, 19); assert.equal(f.builds().length, 2);
  for (const name of ['sing-box', 'hyper-crypt']) assert.equal(fs.readFileSync(join(f.out, name), 'utf8'), 'previous build');
});

test('Docker fallback runs same build and reuses cache', t => {
  const f = new Fixture(t); fs.unlinkSync(join(f.bin, 'go'));
  const containerTools = join(f.base, 'container-tools'); fs.mkdirSync(containerTools);
  f.tool('go', goStub, containerTools); f.env.CONTAINER_GO = containerTools; f.tool('docker', dockerStub);
  f.run('mipsel_24kc'); assertBuildFlags(f.builds());
  const args = JSON.parse(fs.readFileSync(f.env.DOCKER_LOG, 'utf8'));
  for (const value of ['golang:1.25.5-bookworm', 'hyper-router-go-cache:/cache', '-e']) assert(args.includes(value));
  for (const { env } of f.builds()) {
    assert.equal(env.GOMODCACHE, '/cache/mod'); assert.equal(env.GOCACHE, '/cache/build'); assert.equal(env.GOMIPS, 'softfloat');
  }
});
