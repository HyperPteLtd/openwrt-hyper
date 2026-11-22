#!/usr/bin/env node
// SPDX-License-Identifier: GPL-3.0-or-later
/**
 * Verify the bundled mihomo rule sets (etc/hyper/rule-set/*.mrs) against SHA256SUMS;
 * --fetch restores the exact files from the pinned URLs in SOURCES.
 */
import { createHash } from 'node:crypto';
import { readFile, rename, rm, writeFile } from 'node:fs/promises';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// .mrs files are zstd frames around the MRSv1 payload.
const ZSTD = Buffer.from([0x28, 0xb5, 0x2f, 0xfd]);

async function main() {
  const args = process.argv.slice(2);
  if (args.length === 1 && ['--help', '-h'].includes(args[0])) {
    console.log('Usage: node scripts/bundled-rules.mjs [--fetch]');
    return;
  }
  if (args.length > 1 || (args.length === 1 && args[0] !== '--fetch')) {
    throw new Error('Usage: node scripts/bundled-rules.mjs [--fetch]');
  }
  const restore = args[0] === '--fetch';
  const root = join(dirname(fileURLToPath(import.meta.url)), '../luci-app-hyper/root/etc/hyper/rule-set');
  const sums = new Map();
  for (const line of (await readFile(join(root, 'SHA256SUMS'), 'utf8')).split('\n')) {
    const m = line.match(/^([0-9a-f]{64}) [ *]?(\S+)$/);
    if (m) sums.set(m[2], m[1]);
  }
  const sources = new Map();
  for (const url of (await readFile(join(root, 'SOURCES'), 'utf8')).split('\n').filter(Boolean)) {
    const kind = url.match(/\/geo\/(geoip|geosite)\/cn\.mrs$/)?.[1];
    if (!kind || !url.startsWith('https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/')) throw new Error(`Unexpected source: ${url}`);
    sources.set(`${kind}-cn.mrs`, url);
  }
  if (sums.size !== 2 || sources.size !== 2) throw new Error('SHA256SUMS and SOURCES must list geoip-cn.mrs and geosite-cn.mrs');
  for (const [name, sha256] of sums) {
    if (basename(name) !== name || !sources.has(name)) throw new Error(`Invalid rule-set filename: ${name}`);
    const path = join(root, name);
    let data;
    if (restore) {
      const response = await fetch(sources.get(name), { signal: AbortSignal.timeout(30_000) });
      if (!response.ok) throw new Error(`Download failed for ${name}: HTTP ${response.status}`);
      data = Buffer.from(await response.arrayBuffer());
    } else {
      data = await readFile(path);
    }
    if (!data.subarray(0, 4).equals(ZSTD)) throw new Error(`Not a zstd-framed MRS file: ${name}`);
    if (createHash('sha256').update(data).digest('hex') !== sha256) throw new Error(`Checksum mismatch: ${name}`);
    if (restore) {
      const temporary = `${path}.${process.pid}.tmp`;
      try {
        await writeFile(temporary, data, { flag: 'wx', mode: 0o644 });
        await rename(temporary, path);
      } finally {
        await rm(temporary, { force: true });
      }
    }
    console.log(`ok   bundled rule set: ${name}`);
  }
}

try {
  await main();
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
