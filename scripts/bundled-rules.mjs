#!/usr/bin/env node
/** Verify bundled rule sets; --fetch restores the exact manifest-pinned files. */
import { createHash } from 'node:crypto';
import { readFile, rename, rm, writeFile } from 'node:fs/promises';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

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
  const manifest = JSON.parse(await readFile(join(root, 'manifest.json'), 'utf8'));
  for (const [name, item] of Object.entries(manifest.files)) {
    if (basename(name) !== name || !name.endsWith('.srs')) throw new Error(`Invalid rule-set filename: ${name}`);
    const path = join(root, name);
    let data;
    if (restore) {
      const response = await fetch(item.source, { signal: AbortSignal.timeout(30_000) });
      if (!response.ok) throw new Error(`Download failed for ${name}: HTTP ${response.status}`);
      data = Buffer.from(await response.arrayBuffer());
    } else {
      data = await readFile(path);
    }
    if (!data.subarray(0, 3).equals(Buffer.from('SRS'))) throw new Error(`Invalid SRS header: ${name}`);
    if (data.length !== item.bytes) throw new Error(`Size mismatch: ${name}`);
    if (createHash('sha256').update(data).digest('hex') !== item.sha256) throw new Error(`Checksum mismatch: ${name}`);
    if (restore) {
      const temporary = `${path}.${process.pid}.tmp`;
      try {
        await writeFile(temporary, data, { flag: 'wx' });
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
