// Load every page of the LuCI interface in Chinese and in English against fixture.js and check
// that each renders without errors and that the English pages hold no Chinese of the interface's
// own. Screenshots go to HYPER_UI_OUTPUT (build/ui-check).
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
const views = ['overview', 'proxy', 'account', 'rules', 'logs', 'connections', 'plans'];
const han = /[　-〿一-鿿！-｠]/;
// Text the service or the router supplies, not the interface: the language names on the
// language choice read in their own language.
const ownWords = ['简体中文'];

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
			if (english) {
				const lines = text.split('\n').filter((l) => han.test(ownWords.reduce((s, w) => s.replaceAll(w, ''), l)));
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
	// The language choice: English chosen on a Chinese router reloads the page in English and stays.
	const tab = await browser.newPage({ viewport: { width: 1280, height: 900 } });
	await tab.goto(`${base}/?lang=zh-cn#overview`);
	const choice = tab.getByRole('combobox', { name: '界面语言' });
	await choice.waitFor();
	await Promise.all([tab.waitForEvent('load'), choice.selectOption('en-US')]);
	await tab.getByRole('link', { name: 'Overview' }).waitFor();
	await tab.reload();
	await tab.getByRole('link', { name: 'Overview' }).waitFor();
	await tab.getByRole('combobox', { name: 'Interface language' }).selectOption('auto');
	await tab.getByRole('link', { name: '概览' }).waitFor({ timeout: 10000 });
	await tab.close();
} finally {
	await browser.close();
	server.close();
}
if (failures.length) {
	console.error(failures.join('\n'));
	process.exit(1);
}
console.log(`Every page renders in Chinese and English; screenshots in ${output}`);
