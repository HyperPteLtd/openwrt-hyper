// Syntax-check the LuCI interface as the browser loads it: views (view/) are function bodies
// LuCI evaluates, the rest ES modules whose relative imports must exist.
// Usage: node scripts/check-luci.mjs [www root]
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import vm from 'node:vm';

const root = path.resolve(process.argv[2] || 'files/www/luci-static/resources');
const files = [];
(function walk(dir) {
	for (const name of readdirSync(dir)) {
		const file = path.join(dir, name);
		if (statSync(file).isDirectory()) walk(file);
		else if (name.endsWith('.js')) files.push(file);
	}
})(root);

const failures = [];
const work = mkdtempSync(path.join(tmpdir(), 'luci-check-'));
try {
	for (const file of files) {
		const rel = path.relative(root, file);
		const source = readFileSync(file, 'utf8');
		try {
			if (rel.startsWith('view' + path.sep)) {
				new vm.Script('(function(){' + source + '\n})', { filename: rel });
				continue;
			}
			const copy = path.join(work, rel.replaceAll(path.sep, '__') + '.mjs');
			copyFileSync(file, copy);
			execFileSync(process.execPath, ['--check', copy], { stdio: 'pipe' });
			for (const [, spec] of source.matchAll(/(?:\bfrom\s*|\bimport\s*\(\s*)'(\.\.?\/[^']+)'/g)) {
				if (!existsSync(path.resolve(path.dirname(file), spec)))
					throw new Error(`imports missing ${spec}`);
			}
		} catch (e) {
			failures.push(`${rel}: ${(e.stderr?.toString() || e.message).trim()}`);
		}
	}
} finally {
	rmSync(work, { recursive: true, force: true });
}
if (failures.length) {
	console.error(failures.join('\n'));
	process.exit(1);
}
console.log(`${files.length} LuCI scripts parse`);
