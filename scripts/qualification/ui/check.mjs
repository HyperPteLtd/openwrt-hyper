// Load every page of the LuCI interface in Chinese and in English against fixture.js and check
// that each renders without errors and that the English pages hold no Chinese of the interface's
// own, as LuCI's language (the page's lang) decides. Screenshots go to HYPER_UI_OUTPUT
// (build/ui-check).
//   HYPER_PLAYWRIGHT=…/playwright/index.mjs node scripts/qualification/ui/check.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url)),
	root = path.resolve(here, '../../..');
const assets = path.join(root, 'files/www/luci-static/resources/hyper');
const { chromium } = await import(process.env.HYPER_PLAYWRIGHT || 'playwright');
const output = process.env.HYPER_UI_OUTPUT || path.join(root, 'build/ui-check');
await fs.mkdir(output, { recursive: true });

// LuCI writes its language on the page; the fixture plays the router.
const page = (lang, signedOut) =>
	`<!doctype html><html lang="${lang}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
	'<title>Hyper · LuCI</title><link rel="stylesheet" href="/style.css"><style>body{margin:0}</style><div id="app"></div>' +
	`<script type="module">import {mount} from "/app.js";import {fixture} from "/fixture.js";const f=fixture({signedOut:${signedOut}});` +
	'window.model=f;mount(document.querySelector("#app"),async(m,r)=>structuredClone(await f.rpc(m,r)));</script>';
const server = http.createServer(async (req, res) => {
	try {
		const url = new URL(req.url, 'http://127.0.0.1');
		if (url.pathname === '/') {
			res.setHeader('Content-Type', 'text/html;charset=utf-8');
			res.end(page(url.searchParams.get('lang') || 'zh-cn', url.searchParams.has('signedout')));
			return;
		}
		const file = url.pathname === '/fixture.js' ? path.join(here, 'fixture.js') : path.resolve(assets, '.' + url.pathname);
		if (url.pathname !== '/fixture.js' && !file.startsWith(assets + path.sep)) throw Error('path');
		const data = await fs.readFile(file);
		res.setHeader('Content-Type', file.endsWith('.css') ? 'text/css' : file.endsWith('.svg') ? 'image/svg+xml' : 'text/javascript');
		res.end(data);
	} catch {
		res.writeHead(404);
		res.end();
	}
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const base = `http://127.0.0.1:${server.address().port}`;
const views = ['overview', 'proxy', 'account', 'rules', 'logs', 'connections'];
// The core reads as its version alone, never by its release line (scripts/core.lock).
const coreLine = /^CORE_LINE=([a-z][a-z0-9]*)$/m.exec(await fs.readFile(path.join(root, 'scripts/core.lock'), 'utf8'))[1];
const lineName = new RegExp(coreLine, 'i');
const han = /[　-〿一-鿿！-｠]/;

const browser = await chromium.launch();
const failures = [];
try {
	for (const [lang, english] of [['zh-cn', false], ['en', true]]) {
		for (const [view, signedOut] of [...views.map((v) => [v, false]), ['account', true], ['overview', true]]) {
			const name = `${english ? 'en' : 'zh'}-${view}${signedOut ? '-signed-out' : ''}`;
			const tab = await browser.newPage({ viewport: { width: 1280, height: 900 } });
			const errors = [];
			tab.on('pageerror', (e) => errors.push(e.message));
			tab.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
			await tab.goto(`${base}/?lang=${lang}${signedOut ? '&signedout' : ''}#${view}`);
			await tab.locator('.ow-main > *').first().waitFor();
			// Let the page's own reads and its first poll settle.
			await tab.waitForTimeout(1500);
			if (errors.length) failures.push(`${name}: ${errors.join(' | ')}`);
			const text = await tab.evaluate(() => document.body.innerText);
			// A page that throws while it renders shows its error in a card instead.
			if (/无法加载页面|Can't load the page/.test(text)) failures.push(`${name}: the page failed to load: ${text.split('\n').slice(0, 12).join(' ¦ ')}`);
			assert.ok(text.trim().length > 20, `${name}: an empty page`);
			if (lineName.test(text)) failures.push(`${name}: names the core's release line`);
			if (english) {
				const lines = text.split('\n').filter((l) => han.test(l));
				if (lines.length) failures.push(`${name}: Chinese on the English page: ${lines.slice(0, 8).join(' ¦ ')}`);
			} else if (!han.test(text)) {
				failures.push(`${name}: no Chinese on the Chinese page`);
			}
			await tab.screenshot({ path: path.join(output, name + '.png'), fullPage: true });
			await tab.close();
		}
	}
	// The account card's daily check-in, invitations and signed-in devices open in dialogs.
	for (const [lang, english] of [['zh-cn', false], ['en', true]]) {
		const tab = await browser.newPage({ viewport: { width: 1280, height: 900 } });
		const errors = [];
		tab.on('pageerror', (e) => errors.push(e.message));
		await tab.goto(`${base}/?lang=${lang}#overview`);
		const buttons = tab.locator('.ow-account-actions button');
		await buttons.first().waitFor();
		for (const [i, name] of ['checkin', 'invite', 'devices'].entries()) {
			await buttons.nth(i).click();
			const dialog = tab.locator('dialog[open]');
			await dialog.locator('.ow-modal-body [aria-busy="false"], .ow-calendar').first().waitFor();
			await tab.waitForTimeout(300);
			const text = await dialog.innerText();
			if (english && han.test(text)) failures.push(`en ${name}: Chinese in the dialog: ${text.split('\n').filter((l) => han.test(l)).join(' ¦ ')}`);
			await tab.screenshot({ path: path.join(output, `${english ? 'en' : 'zh'}-${name}.png`) });
			await tab.keyboard.press('Escape');
			await dialog.waitFor({ state: 'detached' });
		}
		if (errors.length) failures.push(`${lang} account dialogs: ${errors.join(' | ')}`);
		await tab.close();
	}
	// The core row: its version, 检查更新, then 更新到 the newer stable release; and its storage.
	{
		const tab = await browser.newPage({ viewport: { width: 1280, height: 900 } });
		await tab.goto(`${base}/?lang=zh-cn#overview`);
		const row = tab.locator('.ow-row', { has: tab.getByText('内核', { exact: true }) });
		await row.getByText('当前 v1.0.1', { exact: true }).waitFor();
		await row.getByRole('button', { name: '检查更新', exact: true }).click();
		await row.getByRole('button', { name: '更新到 v1.0.2', exact: true }).click();
		await row.getByText('当前 v1.0.2', { exact: true }).waitFor();
		assert.equal(await tab.evaluate(() => window.model.kernel.selected), 'v1.0.2');
		const storage = tab.locator('.ow-row').filter({ hasText: '内核存储位置' });
		await storage.getByRole('button', { name: '内存', exact: true }).click();
		await storage.getByText('不占闪存，重启后重新下载', { exact: true }).waitFor();
		assert.equal(await tab.evaluate(() => window.model.kernel.mode), 'tmp');
		await tab.close();
	}
	// 直连 with DNS protection keeps DNS alone (dns_only); DNS protection off in 直连 stops it all.
	{
		const tab = await browser.newPage({ viewport: { width: 1280, height: 900 } });
		await tab.goto(`${base}/?lang=zh-cn#overview`);
		const mode = (name) => tab.locator('.ow-modes').getByRole('button', { name, exact: true });
		const dns = tab.getByRole('switch', { name: 'DNS 保护', exact: true });
		const state = () => tab.evaluate(async () => {
			const prefs = await window.model.rpc('network_preferences');
			return { mode: window.model.saved.mode, dns: prefs.dns, dnsOnly: prefs.dns_only };
		});
		await mode('直连').click();
		await tab.locator('.ow-modes small').waitFor();
		let st = await state();
		assert.deepEqual(st, { mode: 'smart', dns: true, dnsOnly: true }, '直连 with DNS protection is DNS-only');
		assert.equal(await mode('直连').getAttribute('aria-pressed'), 'true');
		assert.equal(await dns.getAttribute('aria-checked'), 'true');
		await dns.click();
		await tab.locator('.ow-modes small').waitFor({ state: 'detached' });
		st = await state();
		assert.deepEqual(st, { mode: 'direct', dns: false, dnsOnly: false }, 'DNS protection off in 直连 stops the proxy');
		assert.equal(await dns.getAttribute('aria-checked'), 'false');
		await mode('智能').click();
		await tab.waitForFunction(() => window.model.saved.mode === 'smart');
		st = await state();
		assert.deepEqual(st, { mode: 'smart', dns: false, dnsOnly: false });
		await tab.close();
	}
} finally {
	await browser.close();
	server.close();
}
if (failures.length) {
	console.error(failures.join('\n'));
	process.exit(1);
}
console.log(`Every page renders in Chinese and English; screenshots in ${output}`);
