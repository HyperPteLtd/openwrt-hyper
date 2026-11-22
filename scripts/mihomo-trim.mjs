// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Trim an extracted mihomo source tree to the protocols hyper uses.
//
//   node scripts/mihomo-trim.mjs <mihomo source dir>
//
// Removes the unused `case` branches from the proxy and listener type switches
// (adapter/parser.go, listener/parse.go). Once nothing references a protocol's
// constructor, the Go linker drops it and the dependencies only it pulled in.
// Fails if the source layout changed, so a core update cannot silently ship untrimmed.
import { readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Outbound types written by hyper's config generator: the Hyper service sends AnyTLS nodes
// only (internal/nodes), plus the core's built-in outbounds.
export const KEEP_PROXIES = ['anytls', 'direct', 'dns', 'reject', 'rematch'];
// Router-side inbounds only: no server protocols.
export const KEEP_LISTENERS = ['socks', 'http', 'mixed', 'tproxy', 'redir', 'tunnel', 'tun'];

export function trimSwitch(source, keep, file) {
  const lines = source.split('\n');
  const start = lines.findIndex(line => /^\tswitch proxyType \{$/.test(line));
  if (start < 0) throw new Error(`${file}: switch proxyType not found`);
  const end = lines.findIndex((line, i) => i > start && /^\tdefault:$/.test(line));
  if (end < 0) throw new Error(`${file}: default branch not found`);
  const out = lines.slice(0, start + 1), seen = new Set(), removed = [];
  let keeping = true;
  for (let i = start + 1; i < end; i++) {
    const match = lines[i].match(/^\tcase "([^"]+)":$/);
    if (match) {
      seen.add(match[1]);
      keeping = keep.includes(match[1]);
      if (!keeping) removed.push(match[1]);
    } else if (/^\tcase /.test(lines[i])) {
      throw new Error(`${file}: unexpected case line: ${lines[i]}`);
    }
    if (keeping) out.push(lines[i]);
  }
  const missing = keep.filter(name => !seen.has(name));
  if (missing.length) throw new Error(`${file}: kept types not found: ${missing.join(', ')}`);
  return { text: out.concat(lines.slice(end)).join('\n'), removed };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const dir = process.argv[2];
  if (!dir) { console.error('usage: mihomo-trim.mjs <mihomo source dir>'); process.exit(2); }
  for (const [file, keep] of [['adapter/parser.go', KEEP_PROXIES], ['listener/parse.go', KEEP_LISTENERS]]) {
    const path = join(dir, file);
    const { text, removed } = trimSwitch(readFileSync(path, 'utf8'), keep, file);
    writeFileSync(path, text);
    console.log(`   trim ${file}: removed ${removed.join(' ')}`);
  }
}
