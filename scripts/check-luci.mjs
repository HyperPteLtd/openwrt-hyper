// Syntax-check the LuCI interface as the browser loads it: views (view/) are function bodies
// LuCI evaluates, the rest ES modules whose relative imports must exist. Then its English.
// Usage: node scripts/check-luci.mjs [www root]
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
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
// English (hyper/en.js): every t('…') key the pages use has one and en.js holds no other; no
// Chinese text is left outside t(). Comments may be written in any language.
const hyper = path.join(root, 'hyper');
const { en } = await import(pathToFileURL(path.join(hyper, 'en.js')).href);
const used = new Set();
const unescape = (s) => s.replace(/\\(.)/g, (_, c) => (c === 'n' ? '\n' : c));
for (const file of files) {
	const rel = path.relative(root, file);
	if (!rel.startsWith('hyper' + path.sep) || /[\\/](en|i18n)\.js$/.test(file)) continue;
	let source = readFileSync(file, 'utf8');
	source = source.replace(/\bt\(\s*'((?:[^'\\\n]|\\.)*)'/g, (_, key) => {
		const k = unescape(key);
		used.add(k);
		if (!Object.hasOwn(en, k)) failures.push(`${rel}: no English for ${JSON.stringify(k)}`);
		return "t(''";
	});
	source = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|\s)\/\/.*$/gm, '$1');
	// Regular expressions may match Chinese input.
	source = source.replace(/([(,=:[!&|?]|\breturn)\s*\/(?:\\.|\[(?:\\.|[^\]\\\n])*\]|[^/\\\n[])+\/[a-z]*/g, '$1 /re/');
	source.split('\n').forEach((line, i) => {
		if (/[\u3000-\u303f\u4e00-\u9fff\uff01-\uff60]/.test(line)) failures.push(`${rel}:${i + 1}: Chinese text outside t(): ${line.trim().slice(0, 80)}`);
	});
}
for (const k of Object.keys(en)) if (!used.has(k)) failures.push(`hyper/en.js: unused key ${JSON.stringify(k)}`);
if (failures.length) {
	console.error(failures.join('\n'));
	process.exit(1);
}
console.log(`${files.length} LuCI scripts parse, ${used.size} texts read in English`);
