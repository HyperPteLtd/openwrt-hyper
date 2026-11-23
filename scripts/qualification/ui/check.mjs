import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url)),
	root = path.resolve(here, '../../..');
const assets = path.resolve(process.env.HYPER_UI_ASSETS || path.join(root, 'files/www/luci-static/resources/hyper'));
await fs.access(path.join(assets, 'app.js'));
const { chromium } = await import(process.env.HYPER_PLAYWRIGHT || 'playwright');
const output = process.env.HYPER_UI_OUTPUT || path.join(root, 'build/ui-check');
await fs.mkdir(output, { recursive: true });
const html =
	'<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Hyper · LuCI</title><link rel="stylesheet" href="/style.css"><style>body{margin:0}dialog{margin:0;top:0;left:0}</style><div id="app"></div><script type="module">import {mount} from "/app.js";import {fixture} from "/fixture.js";const qs=new URLSearchParams(location.search);const f=fixture(qs.has("empty"),qs.has("bootstrap"));window.model=f;mount(document.querySelector("#app"),async(m,r)=>structuredClone(await f.rpc(m,r)));</script>';
const server = http.createServer(async (req, res) => {
	try {
		let url = new URL(req.url, 'http://127.0.0.1').pathname;
		if (url === '/') {
			res.setHeader('Content-Type', 'text/html;charset=utf-8');
			res.end(html);
			return;
		}
		if (url === '/favicon.ico') {
			res.writeHead(204);
			res.end();
			return;
		}
		const file = url === '/fixture.js' ? path.join(here, 'fixture.js') : path.resolve(assets, '.' + url);
		if (url !== '/fixture.js' && !file.startsWith(assets + path.sep)) throw Error('path');
		const data = await fs.readFile(file);
		res.setHeader(
			'Content-Type',
			url.endsWith('css') ? 'text/css' : url.endsWith('svg') ? 'image/svg+xml' : 'text/javascript',
		);
		res.end(data);
	} catch {
		res.writeHead(404);
		res.end();
	}
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const browser = await chromium.launch({
	executablePath: process.env.HYPER_CHROME || undefined,
	headless: true,
});
// The overview's kernel row: the title 内核, with the installed version or state underneath.
function kernelRow(page) {
	return page.locator('.ow-row').filter({ has: page.locator('.ow-label > span', { hasText: /^内核$/ }) });
}
// Waits for the overview's power button to show a look: on is the red disconnect control, pulse
// the ring that pulses while the proxy connects or disconnects. The word is the one in view, not
// one still fading out; a phone hides it, so it only has to be in the page.
async function powerShows(page, text, { on = false, pulse = false, name = text } = {}) {
	const look = (on ? '.on' : ':not(.on)') + (pulse ? '.pulse' : ':not(.pulse)');
	await page.locator('.ow-power' + look + '[aria-label="' + name + '"] .ow-power-label > span:not([aria-hidden])')
		.filter({ hasText: new RegExp('^' + text + '$') })
		.waitFor({ state: 'attached' });
}
async function centered(dialog, viewport) {
	const box = await dialog.boundingBox();
	assert.ok(box, 'visible modal');
	assert.ok(Math.abs(box.x + box.width / 2 - viewport.width / 2) <= 1, 'horizontal center');
	assert.ok(Math.abs(box.y + box.height / 2 - viewport.height / 2) <= 1, 'vertical center');
	assert.ok(box.height <= viewport.height - 20 && box.width <= viewport.width - 20, 'dialog fits viewport');
}
async function checkAccountSelection(page, base, {existing = false, count = 1, retry = false} = {}) {
	const query = new URLSearchParams({ scenario: 'login-' + [existing, count, retry].join('-') });
	if (!existing) query.set('empty','');
	await page.goto(base + '/?' + query + '#subscriptions');
	await page.getByRole('button', {name:'＋ 添加订阅'}).click();
	const login = page.getByRole('dialog', {name:'添加订阅'});
	await login.getByRole('button', {name:'服务商登录'}).click();
	await login.getByLabel('服务商地址').fill('https://platform.example');
	await login.getByRole('button', {name:'验证服务商'}).click();
	await login.getByLabel('邮箱').fill('qa@example.invalid');
	await login.getByLabel('密码', {exact:true}).fill('fixture');
	await page.evaluate(({count,retry}) => {
		window.model.controls.loginPlans = count;
		window.model.controls.failRefresh = retry;
	}, {count,retry});
	await login.getByRole('button', {name:'登录',exact:true}).click();
	const sourceWrites = () => page.evaluate(() => window.model.trace.filter(m => m === 'source').length);
	if (existing || count > 1) {
		await login.getByRole('heading', {name:'选择套餐'}).waitFor();
		assert.equal(await page.evaluate(() => window.model.saved.source), existing ? 'p0' : '');
		await login.getByRole('button', {name:'使用',exact:true}).last().click();
	} else if (retry) {
		await login.getByText(/更新失败/).waitFor();
		// A different browser chooses a source while this import awaits a retry.
		await page.evaluate(() => {window.model.saved.source = 'newer-source';});
		await login.getByRole('button', {name:'重新下载'}).click();
	}
	await login.waitFor({state:'detached'});
	assert.equal(await page.evaluate(() => window.model.saved.source), retry ? 'newer-source' : 'account-plan-'+count);
	assert.equal(await sourceWrites(), existing || count > 1 ? 1 : 0);
}
async function checkKernelPreparation(browser, base, viewport) {
	const page = await browser.newPage({viewport});
	try {
		await page.clock.install();
		await page.goto(base + '/?empty&bootstrap');
		const setup = page.getByRole('dialog', {name:'启用全屋代理'});
		await setup.getByText('正在下载', {exact:true}).waitFor();
		const link = setup.getByLabel('粘贴订阅链接');
		await link.fill('https://fixture.invalid/unfinished');
		await page.evaluate(() => {
			window.model.saved.revision++;
			Object.assign(window.model.kernel.bootstrap, {stage:'failed', error:'latest stable release unreachable'});
		});
		await page.clock.runFor(4000);
		await setup.getByText('下载失败', {exact:true}).waitFor();
		assert.equal(await link.inputValue(), 'https://fixture.invalid/unfinished');
		assert.ok(await link.evaluate(element => element === document.activeElement));
		// One button downloads the newest stable kernel; no dialog, no choices.
		await page.evaluate(() => { window.model.controls.kernelInstallError = 'GitHub release API rate limit reached'; });
		await setup.getByRole('button', {name:'重新下载'}).click();
		await setup.getByText('GitHub 查询次数已达上限，请稍后重试').waitFor();
		await page.evaluate(() => { window.model.controls.kernelInstallError = ''; });
		await setup.getByRole('button', {name:'重新下载'}).click();
		await setup.getByText('v0.1.4', {exact:true}).waitFor();
		assert.deepEqual(await page.evaluate(() => window.model.installs), [{tag:'v0.1.4', mode:'double'}]);
		assert.equal(await link.inputValue(), 'https://fixture.invalid/unfinished');
		await page.keyboard.press('Escape');
		await kernelRow(page).getByText('当前 v0.1.4', {exact:true}).waitFor();
		const reads = () => page.evaluate(() => window.model.trace.filter(op => op === 'kernel_read').length);
		await page.clock.runFor(4000); // let the overview finish re-rendering after the setup closes
		const count = await reads();
		await page.clock.runFor(12000);
		assert.equal(await reads(), count, 'installed kernels do not poll the release status');
		await page.goto(base + '/?bootstrap');
		await kernelRow(page).getByText('正在下载', {exact:true}).waitFor();
		await page.evaluate(() => {
			const kernel = window.model.kernel;
			kernel.staged = 'v0.1.5';
			kernel.bootstrap = {id:'initial', action:'kernel/bootstrap', stage:'done',
				result:{staged:kernel.staged, reason:'preflight_failed'}};
		});
		await page.clock.runFor(4000);
		await page.getByRole('button', {name:'使用 v0.1.5'}).click();
		await kernelRow(page).getByText('当前 v0.1.5', {exact:true}).waitFor();
	} finally { await page.close(); }
}
// Outcomes stay where they can be seen: a dialog shows an import failure once and clears its
// status on the next attempt, a confirmation outlives the overview's next re-render, and a card's
// failure that ends after the page changed reaches the alert above the page.
async function checkNotices(browser, base, viewport) {
	const page = await browser.newPage({viewport});
	const errors = [];
	page.on('pageerror', (e) => errors.push(e.message));
	try {
		await page.goto(base + '/?scenario=notice-dialog#subscriptions');
		await page.getByRole('button', {name:'＋ 添加订阅'}).click();
		const login = page.getByRole('dialog', {name:'添加订阅'});
		const status = login.locator('.ow-modal-status');
		await login.getByRole('button', {name:'服务商登录'}).click();
		await login.getByLabel('服务商地址').fill('https://platform.example');
		await page.evaluate(() => { window.model.controls.fail = { subscription_write: 'context deadline exceeded' }; });
		await login.getByRole('button', {name:'验证服务商'}).click();
		await status.filter({hasText:'操作超时'}).waitFor();
		await page.evaluate(() => { delete window.model.controls.fail; });
		await login.getByRole('button', {name:'验证服务商'}).click();
		await login.getByLabel('邮箱').waitFor();
		assert.ok(await status.isHidden(), 'the next attempt clears the dialog status');
		await login.getByLabel('邮箱').fill('qa@example.invalid');
		await login.getByLabel('密码', {exact:true}).fill('fixture');
		await login.getByRole('button', {name:'登录',exact:true}).click();
		await login.getByRole('heading', {name:'选择套餐'}).waitFor();
		await page.evaluate(() => { window.model.controls.failRefresh = true; });
		await login.getByRole('button', {name:'使用',exact:true}).click();
		await login.getByText(/更新失败/).waitFor();
		await page.waitForFunction(() => !document.querySelector('dialog[open] button[aria-busy]'));
		assert.ok(await status.isHidden(), 'the import failure shows once, under the plans');
		await page.keyboard.press('Escape');
		await login.waitFor({state:'detached'});

		await page.goto(base + '/?scenario=notice-overview');
		await page.getByRole('button', {name:'恢复默认',exact:true}).click();
		const restore = page.getByRole('dialog', {name:'恢复默认设置'});
		await restore.getByRole('button', {name:'恢复默认',exact:true}).click();
		await restore.waitFor({state:'detached'});
		const restored = page.locator('.ow-feedback').filter({hasText:'已恢复默认设置'});
		const first = await restored.elementHandle();
		// A change from elsewhere re-renders the overview at its next poll, as a phase change does.
		await page.evaluate(() => { window.model.saved.revision++; });
		await page.waitForFunction(line => !line.isConnected, first);
		assert.equal(await restored.getAttribute('data-tone'), 'done', 'the confirmation outlives the re-render');
		assert.ok(await page.locator('.ow-alert').isHidden(), 'no alert above the page');

		await page.goto(base + '/?scenario=notice-card#subscriptions');
		await page.evaluate(() => { window.model.controls.holdRefresh = true; });
		await page.locator('.ow-source-card').filter({hasText:'外部订阅'}).getByRole('button', {name:'更新',exact:true}).click();
		await page.waitForFunction(() => !!window.model.controls.release);
		await page.getByRole('link', {name:'概览',exact:true}).click();
		await page.getByRole('heading', {name:'故障与恢复',exact:true}).waitFor();
		await page.evaluate(() => { window.model.controls.release('subscription_empty'); });
		await page.locator('.ow-alert').filter({hasText:'订阅内容为空'}).waitFor();
		assert.deepEqual(errors, []);
	} finally { await page.close(); }
}
// Between a phone and a wide page, at 800 px (a 768 px plugin): two subscription cards a row, the
// rule columns stacked, the rule sets still two a row, and nothing wider than the page.
async function checkMiddleWidth(browser, base) {
	const width = 800,
		page = await browser.newPage({viewport: {width, height: 900}, colorScheme: 'light'});
	const errors = [];
	page.on('pageerror', (e) => errors.push(e.message));
	const overflows = () => page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
	try {
		await page.goto(base + '/#subscriptions');
		await page.getByRole('heading', {name:'订阅', exact:true}).first().waitFor();
		const tops = await page.locator('.ow-source-card').evaluateAll(cards => cards.map(card => Math.round(card.getBoundingClientRect().top)));
		assert.equal(tops.filter(top => top === tops[0]).length, 2, 'two subscription cards a row');
		assert.equal(await overflows(), false, 'horizontal overflow in 订阅');
		await page.screenshot({path: path.join(output, '订阅-' + width + '.png'), fullPage: true});
		await page.getByRole('link', {name:'规则', exact:true}).click();
		await page.getByRole('heading', {name:'规则集', exact:true}).waitFor();
		const [builtIn, custom] = await page.locator('.ow-rules > .ow-side-stack').evaluateAll(els => els.map(el => el.getBoundingClientRect().toJSON()));
		assert.ok(builtIn.bottom <= custom.top && Math.abs(builtIn.width - custom.width) < 1, 'rule columns stack across the page');
		const [streaming, domestic] = await page.locator('.ow-set-card').evaluateAll(els => els.map(el => el.getBoundingClientRect().toJSON()));
		assert.ok(Math.abs(streaming.top - domestic.top) < 1 && streaming.right <= domestic.left, 'two rule-set cards a row');
		assert.equal(await overflows(), false, 'horizontal overflow in 规则');
		await page.screenshot({path: path.join(output, '规则-' + width + '.png'), fullPage: true});
		assert.deepEqual(errors, []);
	} finally { await page.close(); }
}
// The overview in the dark scheme, connected: the power button is filled with its dark red, kept
// apart from the theme's lighter danger colour so its white word stays legible, and no text in the
// status card is drawn in the colour behind it.
async function checkDarkOverview(browser, base) {
	const page = await browser.newPage({viewport: {width: 1280, height: 900}});
	const errors = [];
	page.on('pageerror', (e) => errors.push(e.message));
	try {
		await page.emulateMedia({colorScheme: 'dark'});
		await page.goto(base);
		await page.getByRole('heading', {name:'已连接', exact:true}).waitFor();
		await powerShows(page, '断开', {on: true});
		await page.waitForFunction(() => document.querySelector('.ow-power').getAnimations().length === 0);
		const fill = await page.locator('.ow-power').evaluate(el => {
			// A custom property computes to its text; a probe inside the button resolves it as a colour.
			const resolve = (value) => {
				const probe = document.createElement('i');
				probe.style.color = value;
				el.append(probe);
				const color = getComputedStyle(probe).color;
				probe.remove();
				return color;
			};
			return {
				dark: matchMedia('(prefers-color-scheme: dark)').matches,
				background: getComputedStyle(el).backgroundColor,
				red: resolve('var(--ow-power-red)'),
				danger: resolve('var(--ow-danger)'),
			};
		});
		assert.ok(fill.dark, 'the page renders in the dark scheme');
		assert.equal(fill.background, fill.red, 'the connected power button is filled with --ow-power-red');
		assert.notEqual(fill.red, fill.danger, 'the dark scheme gives the power button its own red');
		const clashes = await page.locator('.ow-hero').evaluate(hero => {
			const behind = (el) => {
				for (let node = el; node; node = node.parentElement) {
					const color = getComputedStyle(node).backgroundColor;
					if (color !== 'rgba(0, 0, 0, 0)' && color !== 'transparent') return color;
				}
				return '';
			};
			return [...hero.querySelectorAll('*')]
				.filter(el => el.getClientRects().length && [...el.childNodes].some(n => n.nodeType === Node.TEXT_NODE && n.textContent.trim()))
				.map(el => [el.textContent.trim(), getComputedStyle(el).color, behind(el)])
				.filter(([, color, background]) => color === background);
		});
		assert.deepEqual(clashes, [], 'status card text drawn in the colour behind it');
		await page.screenshot({path: path.join(output, 'overview-dark-1280.png')});
		assert.deepEqual(errors, []);
	} finally { await page.close(); }
}
// Advance the browser clock through ten minutes of normal overview polling.
// Measuring document and chart bounds catches cumulative layout growth that
// a screenshot taken immediately after mount does not show.
async function checkOverviewPolling(page, base, width) {
	await page.clock.install();
	await page.goto(base);
	await page.getByRole('heading', {name: '已连接', exact: true}).waitFor();
	const metrics = () => page.evaluate(() => ({
		document: document.documentElement.scrollHeight,
		scrollY: window.scrollY,
		hero: document.querySelector('.ow-hero').getBoundingClientRect().height,
		chart: document.querySelector('.ow-chart').getBoundingClientRect().height,
		svgs: document.querySelectorAll('.ow-chart svg').length,
		polls: window.model.trace.filter(x => x === 'traffic').length,
	}));
	await page.clock.runFor(5000);
	await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight / 2));
	const first = await metrics();
	for (let i=0; i<10; i++) {
		await page.clock.runFor(60000);
		const state = await metrics();
		assert.equal(state.document, first.document, 'LuCI document grows across polls');
		assert.equal(state.scrollY, first.scrollY, 'polling moves the scroll position');
		assert.equal(state.hero, first.hero, 'overview changes height across polls');
		assert.equal(state.chart, first.chart, 'traffic chart grows across polls');
		assert.equal(state.svgs, 1, 'traffic chart accumulates SVG elements');
		assert.ok(state.polls >= first.polls + 20 * (i+1), 'live data was actually refreshed');
	}
	for (const [phase, heading] of [['protected', '故障保护中'], ['fail_open', '已回退直连'], ['direct', '未连接'], ['running', '已连接']]) {
		await page.evaluate(phase => { window.model.controls.phase = phase; }, phase);
		await page.clock.runFor(3500);
		await page.getByRole('heading', {name: heading, exact: true}).waitFor();
		// A fault keeps the proxy on: the steady red 断开. Asked to proxy, a router still reporting
		// direct has yet to take up the start, so the button reads 连接中.
		if (phase === 'direct') await powerShows(page, '连接中', {pulse: true, name: '连接中，点按断开'});
		else await powerShows(page, '断开', {on: true});
		assert.equal((await metrics()).hero, first.hero, 'fault and stop states keep the card height');
		if (phase === 'protected') {
			await page.getByText('代理流量已暂停，等待内核恢复').waitFor();
			await page.screenshot({ path: path.join(output, 'fault-protection-' + width + '.png') });
		}
	}
	// The service's problem reads under the status line, with a way to the log, not above the page.
	await page.evaluate(() => {
		window.model.controls.phase = 'fail_open';
		window.model.controls.problem = 'network takeover blocked by: service openclash';
	});
	await page.clock.runFor(3500);
	await page.getByRole('heading', {name: '已回退直连', exact: true}).waitFor();
	const problem = page.locator('.ow-connection [role=alert]');
	await problem.filter({hasText: '仍有无法自动处理的冲突：服务 OpenClash'}).waitFor();
	await problem.getByRole('button', {name: '查看日志'}).waitFor();
	await powerShows(page, '断开', {on: true});
	assert.equal(await page.locator('.ow-power').getAttribute('aria-describedby'), await problem.getAttribute('id'));
	assert.equal(await page.locator('.ow-main .ow-warning').count(), 0, 'no banner above the overview');
	await page.screenshot({ path: path.join(output, 'service-problem-' + width + '.png') });
	// The router's retries clear the problem while they start again and set it when they fail: the
	// same problem back in a new line shows without being read out again.
	const announced = await problem.elementHandle();
	await page.evaluate(() => {
		window.model.controls.phase = 'starting';
		delete window.model.controls.problem;
	});
	await page.clock.runFor(3500);
	await page.getByRole('heading', {name: '连接中', exact: true}).waitFor();
	await page.evaluate(() => {
		window.model.controls.phase = 'fail_open';
		window.model.controls.problem = 'network takeover blocked by: service openclash';
	});
	await page.clock.runFor(3500);
	await page.getByRole('heading', {name: '已回退直连', exact: true}).waitFor();
	const retold = page.locator('.ow-connection > .ow-feedback');
	await retold.filter({hasText: '仍有无法自动处理的冲突：服务 OpenClash'}).waitFor();
	assert.ok(await announced.evaluate(line => !line.isConnected), 'the problem is in a new line');
	assert.equal(await retold.getAttribute('role'), 'status', 'an unchanged problem is not announced again');
	await page.evaluate(() => {
		window.model.controls.phase = null;
		delete window.model.controls.problem;
	});
	await page.clock.runFor(3500);
	await page.getByRole('heading', {name: '已连接', exact: true}).waitFor();
	assert.equal(await problem.textContent(), '');
}
const base = 'http://127.0.0.1:' + server.address().port;
try {
	for (const [width, height] of [
		[1280, 900],
		[390, 844],
	]) {
		const page = await browser.newPage({ viewport: { width, height }, colorScheme: 'light' });
		const errors = [];
		page.on('pageerror', (e) => errors.push(e.message));
		page.on('console', (m) => {
			if (['error', 'warning'].includes(m.type())) errors.push(m.text());
		});
		await page.goto(base + '/?empty');
		assert.equal(new URL(page.url()).origin, base);
		assert.equal(await page.title(), 'Hyper · LuCI');
		const setup = page.getByRole('dialog', { name: '启用全屋代理' });
		await setup.waitFor();
		const plugins = setup.locator('.ow-environment > div').filter({ hasText: '其他代理插件' }).locator('strong');
		assert.equal(await plugins.textContent(), 'OpenClash · 启动时自动停用');
		await centered(setup, { width, height });
		await page.screenshot({ path: path.join(output, 'onboarding-' + width + '.png') });
		await page.keyboard.press('Escape');
		await page.locator('dialog[aria-label="启用全屋代理"]').waitFor({ state: 'detached' });
		const reopen = page.getByRole('button', { name: '打开初始化引导' });
		assert.ok(await reopen.evaluate(el => el === document.activeElement));
		assert.ok(await reopen.evaluate(el => !!el.closest('.ow-sources')), 'the setup is offered in the subscription card');
		await page.reload();
		await reopen.waitFor();
		assert.equal(await page.locator('dialog[open]').count(), 0);
		await reopen.click();
		await setup.getByLabel('选择配置文件').setInputFiles({
			name: 'router-fixture.txt', mimeType: 'text/plain', buffer: Buffer.from('socks5://192.0.2.1:1080#fixture'),
		});
		await setup.waitFor({ state: 'hidden' });
		await page.locator('.ow-plan-label').filter({ hasText: 'router-fixture.txt' }).waitFor();
		assert.equal(await page.evaluate(() => window.model.trace.includes('source')), false, 'first file uses backend default');
		await checkAccountSelection(page, base);
		await checkAccountSelection(page, base, {retry:true});
		await checkAccountSelection(page, base, {count:2});
		await checkAccountSelection(page, base, {existing:true});
		await page.goto(base);
		await page.getByRole('heading', { name: '已连接', exact: true }).waitFor();
		const inspect = page.getByRole('button', { name: '检查运行环境' });
		const firewall = page.getByRole('dialog', { name: '防火墙运行环境' });
		await inspect.click();
		await firewall.waitFor();
		assert.equal(await firewall.getByText('可用', {exact:true}).count(), 2);
		assert.doesNotMatch(await firewall.innerText(), /需要处理|软件包/);
		await page.keyboard.press('Escape');
		await page.evaluate(() => {
			window.model.controls.environment = {
				firewall_tproxy: false,
				dns_issue: 'dns_default_upstream lan',
				network_requirements: ['fw4 TProxy target', 'DNS lookup command', 'numeric policy routing and marked route lookup'],
				firewall_packages: ['kmod-nft-tproxy', 'nslookup', 'ip-tiny / ip-full'],
			};
		});
		await inspect.click();
		await firewall.getByText('需要处理', {exact:true}).waitFor();
		assert.match(await firewall.innerText(), /dnsmasq 实例 lan · 存在 Hyper 无法自动接管的默认上游/);
		assert.match(await firewall.innerText(), /fw4 缺少 TProxy 透明代理支持[\s\S]+缺少用于检查 DNS 解析的命令[\s\S]+按流量标记查询路由[\s\S]+kmod-nft-tproxy、nslookup、ip-tiny \/ ip-full/);
		await page.keyboard.press('Escape');
		await page.evaluate(() => delete window.model.controls.environment);
		assert.equal(await page.locator('dialog[open]').count(), 0);
		await page.getByRole('heading', { name: '已连接', exact: true }).waitFor();
		const fallbackLabel = '代理无响应时回退直连';
		const fallback = page.getByRole('switch', {name: fallbackLabel});
		assert.equal(await fallback.getAttribute('aria-checked'), 'true', 'legacy preferences default to fail-open');
		await fallback.click();
		await page.getByRole('switch', {name: fallbackLabel, checked: false}).waitFor();
		assert.equal((await page.evaluate(() => window.model.rpc('network_preferences'))).fail_open, false);
		await page.getByText('内核故障时代理流量中断').waitFor();
		await page.getByRole('link', {name: '代理', exact: true}).click();
		await page.getByRole('heading', {name: '代理', exact: true}).waitFor();
		await page.getByRole('link', {name: '概览', exact: true}).click();
		await page.getByRole('switch', {name: fallbackLabel, checked: false}).waitFor();
		await fallback.click();
		await page.getByRole('switch', {name: fallbackLabel, checked: true}).waitFor();
		assert.equal((await page.evaluate(() => window.model.rpc('network_preferences'))).fail_open, true);
		// A setting that fails to save says so on the line under its row, not above the page,
		// and the next attempt clears it.
		await page.evaluate(() => { window.model.controls.fail = { save_network: 'could not save network preferences' }; });
		const bypass = page.getByRole('switch', {name: '大陆 IP 直连'});
		await bypass.click();
		await page.waitForFunction(() => document.querySelector('[role=switch][aria-label="大陆 IP 直连"]').hasAttribute('aria-describedby'));
		const bypassLine = page.locator('#' + await bypass.getAttribute('aria-describedby'));
		await bypassLine.filter({hasText: '无法保存网络设置'}).waitFor();
		assert.equal(await bypassLine.getAttribute('role'), 'status');
		assert.ok(await bypassLine.evaluate(el => el.previousElementSibling.contains(document.querySelector('[role=switch][aria-label="大陆 IP 直连"]'))), 'the line follows the row');
		assert.ok(await page.locator('.ow-alert').isHidden(), 'no alert above the page');
		await page.evaluate(() => { delete window.model.controls.fail; });
		await bypass.click();
		await page.getByRole('switch', {name: '大陆 IP 直连', checked: true}).waitFor();
		assert.equal(await page.locator('.ow-feedback', {hasText: '无法保存网络设置'}).count(), 0);
		await page.getByRole('switch', {name: '大陆 IP 直连'}).click();
		await page.getByRole('switch', {name: '大陆 IP 直连', checked: false}).waitFor();
		const hero = page.locator('.ow-connection'),
			fixed = (await hero.boundingBox()).height;
		await page.screenshot({ path: path.join(output, 'overview-' + width + '.png'), fullPage: true });
		await page.locator('.ow-row', { hasText: '国内域名' }).getByRole('button', { name: '管理' }).click();
		const dnsDialog = page.getByRole('dialog', { name: '国内域名 DNS' });
		await dnsDialog.getByRole('button', { name: '阿里 DoT' }).click();
		assert.equal(await dnsDialog.getByRole('button', { name: '阿里 DoT' }).getAttribute('aria-pressed'), 'true');
		await page.screenshot({ path: path.join(output, 'dns-dialog-' + width + '.png') });
		await dnsDialog.getByRole('button', { name: '关闭' }).click();
		// 检查更新 offers 更新到 vX only when a newer stable kernel exists.
		const kernel = kernelRow(page);
		await kernel.getByText('当前 v0.1.3', { exact: true }).waitFor();
		await kernel.getByRole('button', { name: '检查更新' }).click();
		const upgrade = kernel.getByRole('button', { name: '更新到 v0.1.4' });
		await upgrade.waitFor();
		await page.screenshot({ path: path.join(output, 'kernel-' + width + '.png') });
		await upgrade.click();
		await kernel.getByRole('button', { name: '已是最新' }).waitFor();
		assert.deepEqual(await page.evaluate(() => window.model.installs), [{ tag: 'v0.1.4', mode: 'double' }]);
		const storage = page.getByRole('group', { name: '内核存储位置' });
		await storage.getByRole('button', { name: '内存' }).click();
		await page.waitForFunction(() => window.model.installs.length === 2);
		assert.deepEqual(await page.evaluate(() => window.model.installs[1]), { tag: 'v0.1.4', mode: 'tmp' });
		await storage.getByRole('button', { name: '闪存' }).click();
		await page.waitForFunction(() => window.model.installs.length === 3);
		await page.evaluate(() => window.model.installs.splice(1));
		await kernel.getByText('当前 v0.1.4', { exact: true }).waitFor();
		await page.getByRole('button', { name: '检测', exact: true }).click();
		for (const probe of ['百度 32 ms', '网易云 41 ms', 'Google 138 ms', 'YouTube 152 ms', 'GitHub 超时']) {
			await page.locator('.ow-checks').getByText(probe, { exact: true }).waitFor();
		}
		// Automatic subscription refresh: a switch, then the hours the user types.
		const hours = page.getByRole('spinbutton', { name: '更新间隔（小时）' });
		await hours.fill('8');
		await hours.press('Enter');
		await hours.blur();
		await page.waitForFunction(async () => (await window.model.rpc('network_preferences')).update_hours === 8);
		await page.getByRole('switch', { name: '订阅自动更新', checked: true }).click();
		await page.getByRole('switch', { name: '订阅自动更新', checked: false }).waitFor();
		assert.equal(await hours.count(), 0);
		await page.getByRole('switch', { name: '订阅自动更新', checked: false }).click();
		await page.getByRole('spinbutton', { name: '更新间隔（小时）' }).waitFor();
		assert.equal((await page.evaluate(() => window.model.rpc('network_preferences'))).update_hours, 8);
		await hours.fill('6');
		await hours.blur();
		await page.waitForFunction(async () => (await window.model.rpc('network_preferences')).update_hours === 6);
		// The running core supports URL probes. Exercise cancellation before
		// completing another batch on the same screen.
		await page.getByRole('link', { name: '代理', exact: true }).click();
		const runningBatch = page.getByRole('button', { name: '全部测速' });
		await runningBatch.waitFor();
		await page.evaluate(() => { window.model.controls.holdProbe = true; });
		await runningBatch.click();
		await page.getByRole('button', { name: /香港 01/ }).first().getByText('测速中').waitFor();
		await page.locator('.ow-proxy-toolbar').getByRole('button', { name: '取消测速' }).click();
		await page.waitForFunction(() => window.model.trace.includes('cancel_tests'));
		await runningBatch.click();
		await page.getByRole('button', { name: /香港 01/ }).first().getByText('87 ms').waitFor();
		assert.equal(await page.getByText('context canceled', { exact: true }).count(), 0);
		await page.getByRole('link', { name: '概览', exact: true }).click();
		// Connected, the whole button is the red disconnect control and holds still; 直连 mode keeps it so.
		const power = page.locator('.ow-power');
		await powerShows(page, '断开', { on: true });
		const modes = page.getByRole('group', { name: '代理模式' });
		await modes.getByRole('button', { name: '直连', exact: true }).click();
		await page.getByRole('heading', { name: '全屋直连', exact: true }).waitFor();
		await powerShows(page, '断开', { on: true });
		await modes.getByRole('button', { name: '智能', exact: true }).click();
		await page.getByRole('heading', { name: '已连接', exact: true }).waitFor();
		// 断开 asks first. A double click asks once: its second click, outside the question, leaves it open.
		const disconnect = page.getByRole('dialog', { name: '断开代理' });
		const focused = () => power.evaluate(el => el === document.activeElement);
		await power.dblclick();
		await disconnect.waitFor();
		assert.equal(await page.locator('dialog[open]').count(), 1, 'a double click keeps the question open');
		// A stop the router refuses says why in the question; the button is the red 断开 again.
		await page.evaluate(() => { window.model.controls.fail = { stop: 'configuration revision changed' }; });
		await disconnect.getByRole('button', { name: '断开', exact: true }).click();
		await disconnect.locator('.ow-modal-status').filter({ hasText: '配置已被其他操作修改，请刷新后重试' }).waitFor();
		await powerShows(page, '断开', { on: true });
		assert.equal(await power.getAttribute('aria-disabled'), null, 'a refused stop leaves 断开 usable');
		await page.evaluate(() => { delete window.model.controls.fail; });
		await page.keyboard.press('Escape');
		await disconnect.waitFor({ state: 'detached' });
		// The stop names the revision current when it is confirmed: a change saved while the question
		// was open does not fail it. While the router lets go the button reads 断开中, its ring ebbs,
		// and it is marked unavailable: a press does nothing. The focus stays on it through the redraws.
		await power.focus();
		await page.keyboard.press('Enter');
		await disconnect.waitFor();
		await page.evaluate(() => {
			window.model.saved.revision++;
			window.model.controls.holdStop = true;
		});
		await disconnect.getByRole('button', { name: '断开', exact: true }).click();
		await disconnect.waitFor({ state: 'detached' });
		await page.getByRole('heading', { name: '断开中', exact: true }).waitFor();
		await powerShows(page, '断开中', { pulse: true });
		assert.ok(await power.evaluate(el => el.classList.contains('gentle') && el.getAnimations().some(a => a.animationName === 'ow-power-ebb')), 'the ring ebbs while disconnecting');
		assert.equal(await power.getAttribute('aria-disabled'), 'true', '断开中 is marked unavailable');
		assert.ok(await focused(), 'the focus returns to the power button after the question');
		await page.keyboard.press('Enter');
		assert.equal(await page.locator('dialog[open]').count(), 0, 'a press while disconnecting opens nothing');
		assert.equal(await power.getAttribute('aria-disabled'), 'true', 'a press while disconnecting keeps it unavailable');
		await page.evaluate(() => { window.model.controls.holdStop = false; });
		await page.getByRole('heading', { name: '未连接', exact: true }).waitFor();
		await powerShows(page, '连接');
		assert.equal(await power.getAttribute('aria-disabled'), null);
		assert.ok(await focused(), 'the focus stays on the power button once disconnected');
		assert.equal((await hero.boundingBox()).height, fixed);
		// Connecting keeps the theme colour and pulses the ring until the core runs, then turns red.
		// Pressed from the keyboard, the button keeps the focus through every redraw.
		await page.evaluate(() => { window.model.controls.holdStart = true; });
		await power.focus();
		await page.keyboard.press('Enter');
		// The router's confirmation counts down every second, between its status reads (every 2.5 s).
		const confirmation = page.locator('.ow-connection > span.ow-muted'),
			statusReads = () => page.evaluate(() => window.model.trace.filter(m => m === 'status').length);
		await confirmation.filter({ hasText: /^已接管流量，确认中 3 秒$/ }).waitFor();
		const reads = await statusReads();
		await confirmation.filter({ hasText: /^已接管流量，确认中 [12] 秒$/ }).waitFor();
		assert.equal(await statusReads(), reads, 'the countdown ticks between status reads');
		await page.getByRole('heading', { name: '连接中', exact: true }).waitFor();
		await powerShows(page, '连接中', { pulse: true, name: '连接中，点按断开' });
		assert.ok(await power.evaluate(el => el.getAnimations().some(a => a.animationName === 'ow-power-pulse')), 'the ring pulses while connecting');
		assert.ok(await focused(), 'the focus follows the power button into the redraw');
		// A redraw keeps the pulse in step: the new ring's cycles start when the old one's did.
		const pulseOrigin = (el) => page.waitForFunction(el => {
			const a = el.getAnimations().find(a => a.animationName === 'ow-power-pulse');
			return a?.startTime != null && a.startTime + a.effect.getTiming().delay;
		}, el).then(v => v.jsonValue());
		const pulsing = await power.elementHandle(),
			origin = await pulseOrigin(pulsing);
		await page.evaluate(() => { window.model.saved.revision++; });
		await page.waitForFunction(el => !el.isConnected, pulsing);
		await powerShows(page, '连接中', { pulse: true, name: '连接中，点按断开' });
		const carried = await pulseOrigin(await power.elementHandle());
		assert.ok(Math.abs(carried - origin) < 34, 'the pulse keeps its timing across a redraw (' + origin + ' → ' + carried + ')');
		await page.evaluate(() => { window.model.controls.holdStart = false; });
		await page.getByRole('heading', { name: '已连接', exact: true }).waitFor();
		await powerShows(page, '断开', { on: true });
		assert.ok(await focused(), 'a connect pressed from the keyboard keeps the focus on the power button');
		// With reduced motion the button changes at once: no pulse, no fades.
		await page.emulateMedia({ reducedMotion: 'reduce' });
		await power.click();
		await page.getByRole('dialog', { name: '断开代理' }).getByRole('button', { name: '断开', exact: true }).click();
		await page.getByRole('heading', { name: '未连接', exact: true }).waitFor();
		await powerShows(page, '连接');
		assert.equal(await power.evaluate(el => el.getAnimations({ subtree: true }).length), 0, 'reduced motion runs no animation');
		await page.emulateMedia({ reducedMotion: null });
		// A start the router refuses says why under the status line, beside the power button, which
		// reads 连接 again.
		await page.evaluate(() => { window.model.controls.fail = { start: 'no authorized primary source' }; });
		await power.click();
		const startLine = page.locator('.ow-connection [role=alert]');
		await startLine.filter({ hasText: '没有可用的主订阅' }).waitFor();
		await powerShows(page, '连接');
		assert.equal(await power.getAttribute('aria-describedby'), await startLine.getAttribute('id'));
		assert.ok(await page.locator('.ow-alert').isHidden(), 'no alert above the page');
		await page.screenshot({ path: path.join(output, 'start-failure-' + width + '.png') });
		await page.evaluate(() => { delete window.model.controls.fail; });
		await page.getByRole('button', { name: '全局', exact: true }).click();
		await page.getByLabel('全局节点').waitFor();
		await page.getByRole('link', { name: '代理', exact: true }).click();
		await page.getByRole('heading', { name: '代理', exact: true }).waitFor();
		assert.equal(await page.locator('.ow-accordion').count(), 0);
		const methods = page.getByRole('group', { name: '测速方式' });
		const batch = page.getByRole('button', { name: '全部测速' });
		assert.equal(await batch.isDisabled(), true, 'URL tests need a running core');
		await methods.getByRole('button', { name: 'TCP' }).click();
		await page.getByRole('button', { name: /新加坡 01/ }).getByText('不支持').waitFor();
		await batch.click();
		await page.getByRole('button', { name: /香港 01/ }).getByText('12 ms').waitFor();
		await methods.getByRole('button', { name: 'URL' }).click();
		assert.equal(await page.getByText('12 ms', { exact: true }).count(), 0);
		await methods.getByRole('button', { name: 'TCP' }).click();
		await page.getByRole('button', { name: /香港 01/ }).getByText('12 ms').waitFor();
		await page.getByRole('button', { name: /日本 02/ }).click();
		await page.waitForFunction(() => window.model.choices['@global']?.saved.endsWith('2'));
		await page.getByRole('link', { name: '概览', exact: true }).click();
		await page.getByRole('button', { name: '智能', exact: true }).click();
		if (width < 650) {
			// A phone shows the subscription card's menu and its line, which stays a live region while empty.
			assert.notEqual(await page.locator('.ow-sources > .ow-feedback').evaluate(el => getComputedStyle(el).display), 'none');
			await page.getByLabel('切换订阅', { exact: true }).click();
		}
		await page.getByRole('button', { name: '切换至 外部订阅' }).click();
		await page.waitForFunction(() => window.model.saved.source === 'p1');
		await page.getByRole('link', { name: '代理', exact: true }).click();
		const search = page.getByRole('searchbox', { name: '搜索节点或策略组' });
		await search.fill('日本');
		await page.locator('details[open]').first().waitFor();
		await search.fill('');
		await page.screenshot({ path: path.join(output, 'proxy-' + width + '.png'), fullPage: true });
		for (const name of ['订阅', '规则', '日志', '连接记录']) {
			await page.getByRole('link', { name, exact: true }).click();
			await page.getByRole('heading', { name: name === '规则' ? '自定义规则' : name, exact: true }).first().waitFor();
			if (name === '订阅') {
				// Every card names its kind in words. Its data-kind is what the stylesheet keys on;
				// only the provider account's is platform, the card set apart from the rest.
				const cardKinds = await page.locator('.ow-source-card').evaluateAll(cards => cards.map(card =>
					[card.dataset.kind, card.querySelector('.ow-source-kind')?.textContent]));
				assert.deepEqual(cardKinds, [
					['platform', '服务商账户'],
					['external', '链接订阅'],
					['external', '链接订阅'],
					['external', '链接订阅'],
					['file', '本地文件'],
				], 'subscription kinds');
				// A file has no automatic update; its card imports the file again.
				const fileCard = page.locator('.ow-source-card[data-kind="file"]');
				assert.equal(await fileCard.locator('.ow-source-meta').textContent(), 'Clash YAML');
				await fileCard.getByRole('button', {name:'导入新文件', exact:true}).waitFor();
				// Three cards a row on a wide page and one on a phone; a row's cards share one height.
				const boxes = await page.locator('.ow-source-card').evaluateAll(cards => cards.map(card => {
					const box = card.getBoundingClientRect(),
						buttons = card.querySelector(':scope > .ow-actions').getBoundingClientRect();
					return [Math.round(box.top), Math.round(box.height), Math.round(buttons.bottom)];
				}));
				const firstRow = boxes.filter(([top]) => top === boxes[0][0]);
				assert.equal(firstRow.length, width > 1000 ? 3 : 1, 'subscription cards per row');
				assert.ok(firstRow.every(([, height]) => height === firstRow[0][1]), 'a row of subscription cards shares one height');
				// The provider card carries a plan and a notice its neighbours lack; their buttons still line up along the foot.
				assert.ok(firstRow.every(([, , buttons]) => buttons === firstRow[0][2]), 'a row of subscription cards lines up its buttons');
				// Every card has an 更新 button; each is described by its subscription's name.
				const updates = await page.locator('.ow-source-card').evaluateAll(cards => cards.slice(0, 2).map(card => {
					const update = [...card.querySelectorAll('button')].find(b => b.textContent === '更新');
					return (update.getAttribute('aria-describedby') || '').split(/\s+/).map(id => document.getElementById(id)?.textContent).join(' ');
				}));
				assert.deepEqual(updates, ['示例服务商 · 标准套餐', '外部订阅'], 'card buttons are described by their subscription');
				const clockProblem = page.locator('.ow-source-card').filter({hasText:'备用订阅'});
				await clockProblem.getByText(/证书尚未生效.*1970-01-02.*2026-01-01.*2027-01-01.*UTC/).waitFor();
				const savedProblem = page.locator('.ow-source-card').filter({hasText:'家庭套餐'});
				await savedProblem.locator('details summary').filter({hasText:'1 条导入提醒'}).click();
                await savedProblem.locator('details p').filter({hasText:/Media.*SRS.*该规则集无法被内核直接使用/}).waitFor();
				// An update an automatic refresh could not apply waits on its card. A button among the
				// card's runs the card's update to review it, while the line above reports the update.
				await savedProblem.getByRole('status').filter({hasText:'自动更新发现变更待确认，确认前节点和规则保持不变 · 会移除原有的拦截规则'}).waitFor();
				const pendingButton = savedProblem.locator('.ow-actions').getByRole('button', {name:'查看并确认', exact:true});
				const savedUpdate = savedProblem.getByRole('button', {name:'更新', exact:true});
				// While it runs, neither it nor 更新 starts another.
				await page.evaluate(() => { window.model.controls.holdRefresh = true; delete window.model.controls.release; });
				await pendingButton.click();
				await page.waitForFunction(() => !!window.model.controls.release);
				assert.equal(await savedUpdate.getAttribute('aria-busy'), 'true', 'the card\'s 更新 reads busy too');
				const refreshCount = () => page.evaluate(() => window.model.refreshes.length);
				const started = await refreshCount();
				await savedUpdate.dispatchEvent('click');
				assert.equal(await refreshCount(), started, 'a second press starts no second update');
				await page.evaluate(() => { window.model.controls.release('subscription_empty'); });
				await savedProblem.getByText('更新失败 · 订阅内容为空 · 已保留上次配置', {exact:true}).waitFor();
				await page.waitForFunction(() => !document.querySelector('.ow-source-card button[aria-busy]'));
				// Cancelled, its review keeps the subscription as it is and returns to the button.
				await pendingButton.click();
				const pendingReview = page.getByRole('dialog', {name:'确认订阅变更'});
				await pendingReview.getByText('会移除原有的拦截规则', {exact:true}).waitFor();
				await pendingReview.getByRole('button', {name:'取消', exact:true}).click();
				await pendingReview.waitFor({state:'detached'});
				await savedProblem.getByText('已保留现有配置', {exact:true}).waitFor();
				assert.ok(await pendingButton.evaluate(el => el === document.activeElement), 'a cancelled review returns to its button');
				// Applied, it replaces the card, and focus moves on to the new card's 更新.
				const pendingCard = await savedProblem.elementHandle();
				await pendingButton.click();
				await pendingReview.getByRole('button', {name:'应用此版本', exact:true}).click();
				await pendingReview.waitFor({state:'detached'});
				await page.waitForFunction(card => !card.isConnected, pendingCard);
				assert.equal(await savedProblem.getByText(/自动更新发现变更待确认/).count(), 0, 'an applied update is no longer pending');
				assert.ok(await savedUpdate.evaluate(el => el === document.activeElement), 'focus moves to the new card');
				// A version the router cannot use: 查看原因 shows why on the card, and focus stays on it.
				const blocked = page.locator('.ow-source-card').filter({hasText:'外部订阅'});
				await blocked.getByText('自动更新取到的新版本无法使用，已保留上次配置 · 新版本没有分流规则和策略组', {exact:true}).waitFor();
				const why = blocked.getByRole('button', {name:'查看原因', exact:true});
				await why.click();
				const whySummary = blocked.locator('details summary').filter({hasText:'已保留上次配置'});
				await whySummary.waitFor();
				await page.waitForFunction(() => !document.querySelector('.ow-source-card button[aria-busy]'));
				assert.ok(await why.evaluate(el => el === document.activeElement), 'the reason opens beside its button');
				assert.equal(await page.locator('dialog[open]').count(), 0, 'the reason stays on the card');
				await whySummary.click();
				await blocked.locator('details p').filter({hasText:'新版本没有分流规则和策略组'}).waitFor();
                const account = page.locator('.ow-source-card').filter({ hasText: '示例服务商' });
				await account.getByText('1 条导入提醒', {exact:true}).click();
				const aggregate = account.locator('details[open] p');
				await aggregate.getByText(/服务商报告了 1 条模板提醒，仅提供了总数/).waitFor();
				assert.doesNotMatch(await aggregate.innerText(), /^1\s*·/);
				const startReads = await page.evaluate(() => window.model.reads.length);
				await account.getByRole('button', {name:'更新',exact:true}).click();
				await page.waitForFunction(start => window.model.reads.length > start, startReads);
				assert.deepEqual(await page.evaluate(start => window.model.reads.slice(start), startReads), ['status']);
				await account.getByText('5 条导入提醒', {exact:true}).click();
				await account.getByText('策略组已按默认设置固定为拦截（2 条）', {exact:true}).waitFor();
				assert.doesNotMatch(await account.innerText(), /STATIC_REJECT|1\s*·\s*策略组/);
				await account.getByText('策略组没有可用节点，服务商已将出口设为直连（3 条）', {exact:true}).waitFor();
				await page.evaluate(() => {window.model.controls.providerFailure = 'provider_http_404';});
				await account.getByRole('button',{name:'更新',exact:true}).click();
				await account.getByText('更新失败 · 规则提供方 · geosite-cn.mrs · 返回 HTTP 404 · 已保留上次配置', {exact:true}).waitFor();
				await page.waitForFunction(() => !document.querySelector('.ow-source-card button[aria-busy]'));
				assert.ok(await page.locator('.ow-alert').isHidden(), 'the card failure is not repeated above the page');
				await page.getByRole('link',{name:'概览',exact:true}).click();
				await page.getByRole('link',{name:'订阅',exact:true}).click();
				await account.getByText('上次更新失败 · 规则提供方 · geosite-cn.mrs · 返回 HTTP 404 · 已保留上次配置', {exact:true}).waitFor();
				await page.evaluate(() => {window.model.controls.providerFailure = 'invalid_rule_provider';});
				await account.getByRole('button',{name:'更新',exact:true}).click();
				await account.getByText(/更新失败 · 规则提供方 · geosite-cn\.mrs · 规则文件格式无效.*已保留上次配置/).waitFor();
				await page.evaluate(() => { window.model.controls.downloadOffer=true; });
				await account.getByRole('button',{name:'更新',exact:true}).click();
				const failedSourceCard=await account.elementHandle();
				await account.getByRole('button',{name:'选择规则下载出口'}).click();
				const retry=page.getByRole('dialog',{name:'选择规则下载出口'});
				await retry.getByRole('combobox',{name:'规则下载出口'}).selectOption('@direct');
				await retry.getByRole('button',{name:'保存并重试'}).click();
				await page.waitForFunction(()=>window.model.controls.downloadChoice?.member==='@direct');
				await retry.waitFor({state:'detached'});
				await page.waitForFunction(card=>!card.isConnected,failedSourceCard);
				await account.getByRole('button',{name:'更新',exact:true}).waitFor();
				await page.evaluate(() => {window.model.controls.partialReview = true;});
				await account.getByRole('button',{name:'更新',exact:true}).click();
				const partial = page.getByRole('dialog',{name:'确认订阅变更'});
				await partial.getByText(/跳过这些条目，保留其余分流/).waitFor();
				await partial.getByText(/此次变更会跳过拦截规则.*继续匹配其余规则和兜底出口/).waitFor();
				const warningTint = () => partial.locator('.ow-warning').evaluate(el => getComputedStyle(el).backgroundColor);
				const lightTint = await warningTint();
				await page.emulateMedia({ colorScheme: 'dark' });
				assert.notEqual(await warningTint(), lightTint, 'the review warning takes dark colours');
				await page.emulateMedia({ colorScheme: 'light' });
				await partial.getByText(/Streaming · SRS.*无法被内核直接使用/).waitFor();
                await partial.getByText(/rule 1 · Streaming.*此拦截规则引用的规则集未载入/).waitFor();
                assert.equal(await partial.getByRole('checkbox').count(), 0);
				await partial.getByRole('button',{name:'保留其余分流并应用'}).click();
				await partial.waitFor({state:'detached'});
				await page.waitForFunction(() => window.model.controls.partialReview === false);

				const all = page.getByRole('button', {name:'全部更新',exact:true});
				const before = await page.evaluate(() => window.model.trace.filter(m => m === 'subscription_write').length);
				await all.click();
				await page.waitForFunction(n => window.model.trace.filter(m => m === 'subscription_write').length === n+4, before);
				await page.waitForFunction(() => document.querySelector('.ow-main[aria-busy=false]') && ![...document.querySelectorAll('button[aria-busy]')].some(el => el.textContent === '全部更新'));
				assert.equal(await page.evaluate(() => window.model.maxRefreshTasks), 2);
				// The subscriptions that failed are named under 全部更新 once the page has re-rendered.
				await page.evaluate(() => { window.model.controls.failRefresh = true; });
				await all.click();
				await page.locator('.ow-toolbar + .ow-feedback').filter({hasText: '更新失败 · 示例服务商 · 标准套餐'}).waitFor();
				assert.ok(await page.locator('.ow-alert').isHidden(), 'no alert above the page');

				await account.getByText('Hyper YAML · 每 6 小时自动更新', { exact: true }).waitFor();
				assert.equal(await account.getAttribute('data-kind'), 'platform');
				assert.equal(await account.locator('.ow-source-kind').innerText(), '服务商账户');
				await account.getByRole('button', { name: '编辑', exact: true }).click();
				const edit = page.getByRole('dialog', { name: '编辑订阅' });
				assert.equal(await edit.getByRole('combobox', { name: '订阅请求 UA' }).count(), 0);
				await edit.getByRole('button', { name: '保存', exact: true }).click();
				await edit.waitFor({ state: 'hidden' });
				// Signing out sits on the card, outside the editor.
				const leaveAccount = account.getByRole('button', { name: '退出账户' });
				await leaveAccount.focus();
				await page.keyboard.press('Enter');
				const confirm = page.getByRole('dialog', { name: '退出服务商账户' });
				await centered(confirm, { width, height });
				await page.keyboard.press('Escape');
				await confirm.waitFor({ state: 'detached' });
				assert.ok(await leaveAccount.evaluate(el => el === document.activeElement), 'escape returns to the card button');
				await page.evaluate(() => { window.model.controls.longRefresh = true; });
				const external = page.locator('.ow-source-card').filter({ hasText: '外部订阅' });
				assert.equal(await external.locator('.ow-source-kind').innerText(), '链接订阅');
				await external.getByRole('button', { name: '编辑' }).click();
				// The editor shows the current link; a changed link is verified before it replaces the old one.
				const address = edit.getByLabel('订阅链接');
				assert.equal(await address.inputValue(), 'https://fixture.invalid/current');
				await address.fill('https://fixture.invalid/replacement');
				await edit.getByRole('button', { name: '保存', exact: true }).click();
				const failure = page.getByRole('dialog', { name: '已保留上次配置' });
				await centered(failure, { width, height });
				await failure.getByText('测试节点 24 · 这条规则无法转换为内核规则').scrollIntoViewIfNeeded();
				assert.ok(await failure.locator('.ow-modal-body').evaluate(el => el.scrollTop > 0));
				const header = await failure.locator('.ow-modal-head').boundingBox(), frame = await failure.boundingBox();
				assert.ok(Math.abs(header.y - frame.y) <= 2, 'long content keeps dialog controls visible');
				await page.screenshot({path: path.join(output, 'subscription-review-' + width + '.png')});
				await failure.getByRole('button', { name: '关闭' }).click();
				await failure.waitFor({ state: 'detached' });
				await edit.waitFor();
				assert.equal(await address.inputValue(), 'https://fixture.invalid/replacement');
				await page.keyboard.press('Escape');
				await edit.waitFor({state: 'detached'});
				// A routing choice is saved with the update read with it. Switched and then cancelled at
				// its confirmation, the subscription keeps the routing it uses, and the editor shows that.
				await external.getByRole('button', { name: '编辑' }).click();
				const routing = edit.getByRole('combobox', { name: '分流规则', exact: true });
				assert.equal(await routing.inputValue(), '');
				await routing.selectOption('template');
				await edit.getByRole('button', { name: '保存', exact: true }).click();
				const routingReview = page.getByRole('dialog', { name: '确认订阅变更' });
				await routingReview.getByText('会移除原有的拦截规则', { exact: true }).waitFor();
				await routingReview.getByRole('button', { name: '取消', exact: true }).click();
				await routingReview.waitFor({ state: 'detached' });
				await edit.locator('button[type="submit"]:not([aria-busy])').waitFor();
				assert.equal(await routing.inputValue(), '', 'a cancelled switch leaves the editor on the routing in use');
				await page.keyboard.press('Escape');
				await edit.waitFor({ state: 'detached' });
				const keptCard = await external.elementHandle();
				await page.getByRole('link', { name: '概览', exact: true }).click();
				await page.waitForFunction(card => !card.isConnected, keptCard);
				await page.getByRole('link', { name: '订阅', exact: true }).click();
				await external.getByRole('button', { name: '编辑' }).click();
				assert.equal(await routing.inputValue(), '', 'a cancelled switch is not saved');
				// Applied, the switch is saved with its update.
				await routing.selectOption('template');
				const switchedCard = await external.elementHandle();
				await edit.getByRole('button', { name: '保存', exact: true }).click();
				await routingReview.getByRole('button', { name: '应用此版本', exact: true }).click();
				await edit.waitFor({ state: 'detached' });
				await page.waitForFunction(card => !card.isConnected, switchedCard);
				await external.getByRole('button', { name: '编辑' }).click();
				assert.equal(await routing.inputValue(), 'template', 'an applied switch is saved');
				await page.keyboard.press('Escape');
				await edit.waitFor({ state: 'detached' });
				// Updated from its card, a version the router keeps back opens its review on the card.
				await page.evaluate(() => { window.model.controls.longRefresh = true; });
				await external.getByRole('button', { name: '更新', exact: true }).click();
				await external.locator('details summary').filter({ hasText: '已保留上次配置' }).click();
				await external.getByText('测试节点 24 · 这条规则无法转换为内核规则').waitFor();
				assert.equal(await page.locator('dialog[open]').count(), 0, 'the review stays on the card');
				// A file is not downloaded again: a new routing choice asks for the file, which is imported
				// again with it.
				const offline = page.locator('.ow-source-card').filter({ hasText: '离线配置' });
				await offline.getByRole('button', { name: '编辑' }).click();
				const offlineFile = edit.getByLabel('选择配置文件');
				await routing.waitFor();
				assert.ok(await offlineFile.isHidden(), 'the routing in use needs no file');
				await routing.selectOption('template');
				await offlineFile.waitFor();
				assert.ok(await offlineFile.evaluate(el => el.required && !el.disabled), 'a new routing choice requires the file');
				await offlineFile.setInputFiles({ name: 'offline.yaml', mimeType: 'text/plain', buffer: Buffer.from('proxies: []\n') });
				const offlineCard = await offline.elementHandle();
				await edit.getByRole('button', { name: '保存', exact: true }).click();
				await routingReview.getByRole('button', { name: '应用此版本', exact: true }).click();
				await edit.waitFor({ state: 'detached' });
				await page.waitForFunction(card => !card.isConnected, offlineCard);
				assert.deepEqual(await page.evaluate(() => window.model.refreshes.filter(r => r.source === 'p4')), [
					{ source: 'p4', content: 'proxies: []\n', rules: 'template' },
					{ source: 'p4', content: 'proxies: []\n', confirm: 'routing', rules: 'template' },
				], 'the file goes with the routing choice');
				await page.getByRole('button', { name: '＋ 添加订阅' }).click();
				const dialog = page.getByRole('dialog');
				await centered(dialog, { width, height });
				const ua = dialog.getByRole('combobox', { name: '订阅请求 UA' });
				assert.deepEqual(await ua.locator('option').evaluateAll(items => items.map(item => item.value)), [
					'clash', 'sing-box', 'Hyper',
				]);
				assert.equal(await ua.inputValue(), 'clash');
				assert.equal(await dialog.getByPlaceholder('留空使用订阅名称或域名').getAttribute('required'), null);
				await page.keyboard.press('Escape');
			}
			if (name === '规则') {
				// Built-in rule sets and custom rules show together: side by side when wide, stacked on a phone.
				await page.getByRole('heading',{name:'规则集',exact:true}).waitFor();
				const [builtIn, custom] = await page.locator('.ow-rules > .ow-side-stack').evaluateAll(els => els.map(el => el.getBoundingClientRect().toJSON()));
				if (width > 900) assert.ok(builtIn.right <= custom.left && builtIn.width > custom.width, 'built-in rules sit left of custom rules');
				else assert.ok(builtIn.bottom <= custom.top, 'rule columns stack on a phone');
				// One card per rule set: two a row (left and right) in the built-in column, one a row on a phone.
				const setCard = tag => page.locator('.ow-set-card').filter({has:page.getByRole('heading',{name:tag,exact:true})});
				const [streaming, domestic] = await page.locator('.ow-set-card').evaluateAll(els => els.map(el => el.getBoundingClientRect().toJSON()));
				assert.ok(builtIn.left <= streaming.left && domestic.right <= builtIn.right, 'rule-set cards stay in the built-in column');
				if (width > 900) assert.ok(Math.abs(streaming.top - domestic.top) < 1 && streaming.right <= domestic.left, 'two rule-set cards a row');
				else assert.ok(streaming.bottom <= domestic.top, 'one rule-set card a row on a phone');
				// Custom rules fill the right column, or follow the rule sets on a phone. Both are narrow:
				// a long match wraps inside the column, and each rule's buttons move under its match.
				const ruleRows = await page.locator('.ow-rules-custom .ow-rule-row').evaluateAll(rows => rows.map(row => {
					const match = row.querySelector('.ow-rule-match'),
						box = row.getBoundingClientRect();
					return {
						left: box.left,
						right: box.right,
						lines: match.querySelector('strong').getClientRects().length,
						overflow: match.scrollWidth > match.clientWidth,
						under: row.querySelector(':scope > .ow-actions').getBoundingClientRect().top >= match.getBoundingClientRect().bottom,
					};
				}));
				assert.equal(ruleRows.length, 3, 'one row per custom rule');
				assert.ok(ruleRows.every(row => custom.left <= row.left && row.right <= custom.right), 'custom rules sit in their column');
				assert.ok(ruleRows[1].lines > 1 && ruleRows.every(row => !row.overflow), 'a long match wraps inside the column');
				assert.ok(ruleRows.every(row => row.under), 'buttons sit under the match in a narrow column');
				// A move that fails to save says so on the line under its rule, which describes the button.
				await page.evaluate(() => { window.model.controls.fail = { save_rules: 'configuration revision changed' }; });
				const lower = page.getByRole('button', {name:'下移第 1 条规则', exact:true});
				await lower.click();
				const moveLine = page.locator('.ow-rules-custom .ow-rule-row').first().locator('.ow-feedback');
				await moveLine.filter({hasText:'配置已被其他操作修改，请刷新后重试'}).waitFor();
				assert.equal(await moveLine.getAttribute('role'), 'status');
				assert.equal(await lower.getAttribute('aria-describedby'), await moveLine.getAttribute('id'));
				assert.ok(await page.locator('.ow-alert').isHidden(), 'no alert above the page');
				await page.evaluate(() => { delete window.model.controls.fail; });
				await setCard('Streaming').getByText('订阅',{exact:true}).waitFor();
				await setCard('Streaming').getByText('二进制',{exact:true}).waitFor();
				assert.match(await setCard('Streaming').locator('dl').innerText(), /条目\s*1 条\s*命中连接\s*3 次\s*去向\s*直连/);
				assert.match(await setCard('Domestic').locator('dl').innerText(), /命中连接\s*—/);
				// GEOSITE,CN uses the plugin's mainland domain list: a built-in card under its Chinese
				// name, with the hits the core counts for that list.
				const china = setCard('大陆域名');
				await china.getByText('内置',{exact:true}).waitFor();
				assert.match(await china.locator('dl').innerText(), /条目\s*每周自动更新\s*命中连接\s*2 次\s*去向\s*直连/);
				assert.equal(await page.locator('.ow-set-card h3').filter({hasText:'hyper'}).count(), 0, 'no rule set shows by an internal name');
				// GEOIP,CN uses the mainland IP list, the other built-in card.
				const chinaIP = setCard('大陆 IP');
				await chinaIP.getByText('内置',{exact:true}).waitFor();
				assert.match(await chinaIP.locator('dl').innerText(), /条目\s*每周自动更新\s*命中连接\s*—\s*去向\s*直连/);
				// A mainland list's details look a name up in the list by its tag, list the rules that
				// use it by its Chinese name, and lead to the connections the core reports for it.
				await china.getByRole('button',{name:'详情'}).click();
				const chinaDetail = page.getByRole('dialog',{name:'大陆域名'});
				await chinaDetail.getByText('内置，每周自动更新',{exact:true}).waitFor();
				await chinaDetail.getByRole('searchbox',{name:'检查是否命中'}).fill('www.baidu.com');
				await chinaDetail.getByRole('button',{name:'检查',exact:true}).click();
				await chinaDetail.getByText('www.baidu.com 命中此规则集',{exact:true}).waitFor();
				await chinaDetail.locator('.ow-rule-row').filter({hasText:'RULE-SET,大陆域名'}).waitFor();
				assert.doesNotMatch(await chinaDetail.innerText(), /hyper/);
				await chinaDetail.getByRole('button',{name:'查看命中的连接 ›'}).click();
				await page.locator('.ow-connections').getByText('www.baidu.com:443').waitFor();
				const ruleSets = page.getByRole('combobox',{name:'命中规则集'});
				assert.equal(await ruleSets.inputValue(), 'hyper-dns-cn');
				assert.equal(await ruleSets.locator('option:checked').textContent(), '大陆域名');
				assert.equal(await page.locator('.ow-connections tbody tr').count(), 1, 'only the mainland list\'s connections');
				await page.getByRole('link',{name:'规则',exact:true}).click();
				await page.getByRole('heading',{name:'规则集',exact:true}).waitFor();
				await setCard('Streaming').getByRole('button',{name:'详情'}).click();
				const detail = page.getByRole('dialog',{name:'Streaming'});
				await detail.getByText('引用此规则集的规则').waitFor();
				await page.keyboard.press('Escape');
				await setCard('Streaming').getByRole('button',{name:'连接记录'}).click();
				assert.equal(await page.getByRole('combobox',{name:'命中规则集'}).inputValue(), 'Streaming');
			}
			if (name === '日志') {
				const row = page.locator('.ow-log').filter({has:page.getByText('内核发起连接时，系统报告网络不可达 ×3', {exact:true})});
				await row.locator('summary').click();
				await row.getByText(/代理内核 · .*network_unreachable/).waitFor();
				const filter = page.getByRole('searchbox', {name:'搜索日志',exact:true});
				for (const query of ['network_unreachable', '网络不可达']) {
					await filter.fill(query);
					assert.equal(await page.locator('.ow-log').count(), 1);
					await row.waitFor();
				}
				await filter.fill('');
				const stopped = page.locator('.ow-log').filter({has:page.getByText('已停用冲突的代理插件：OpenClash、PassWall 2', {exact:true})});
				await stopped.locator('summary').click();
				assert.equal(await stopped.locator('.ow-log-detail').textContent(), 'openclash, passwall2');
				// The core's own words show once, in the entry's sentence; a task's line reads by its
				// name and stage, and the code it was held for.
				const coreWords = page.locator('.ow-log').filter({has:page.getByText('内核报告：tcp keepalive not supported on <addr>', {exact:true})});
				await coreWords.locator('summary').click();
				await coreWords.getByText(/代理内核 · .*core_diagnostic/).waitFor();
				assert.equal(await coreWords.locator('.ow-log-detail').count(), 0, 'the core\'s words show once');
				await page.locator('.ow-log summary').getByText('更新订阅 · 未应用 · 订阅有变更待确认，请到订阅页查看并确认', {exact:true}).waitFor();
			}
			if (name === '连接记录') {
				// The router's own set and QUIC rule read in Chinese, in the table and the set filter.
				const ruleSets = page.getByRole('combobox', {name:'命中规则集'});
				await ruleSets.selectOption({label:'全部规则集'});
				const ruleCell = (target) => page.locator('.ow-connections tbody tr').filter({hasText:target}).locator('td[data-label="命中规则"]');
				assert.equal(await ruleCell('play.googleapis.cn:443').textContent(), '规则集 需代理的大陆域名');
				assert.equal(await ruleCell('video.example:443').textContent(), '拦截境外 QUIC');
				assert.deepEqual(await ruleSets.locator('option').allTextContents(), ['全部规则集', '需代理的大陆域名', '大陆域名']);
				assert.equal(await page.locator('.ow-connections').getByText(/hyper|RULE-SET/).count(), 0, 'no internal set or raw rule');
			}
			await page.screenshot({ path: path.join(output, name + '-' + width + '.png'), fullPage: true });
			assert.equal(
				await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
				false,
				'horizontal overflow in ' + name,
			);
		}
		await page.getByRole('button', { name: '清空', exact: true }).click();
		await page.getByRole('dialog').getByRole('button', { name: '清空连接记录', exact: true }).click();
		await page.getByText('暂无连接记录', { exact: true }).waitFor();
		await page.getByRole('link', { name: '日志', exact: true }).focus();
		await page.keyboard.press('Enter');
		await page.getByText('local_core_unavailable', { exact: true }).waitFor();
		await page.goBack();
		await page.getByText('暂无连接记录', { exact: true }).waitFor();
		await page.reload();
		await page.getByRole('heading', { name: '连接记录', exact: true }).waitFor();
		assert.equal(await page.title(), 'Hyper · LuCI');
		assert.equal(await page.locator('vite-error-overlay, nextjs-portal, #webpack-dev-server-client-overlay').count(), 0);
		await checkOverviewPolling(page, base, width);
		await checkKernelPreparation(browser, base, {width,height});
		await checkNotices(browser, base, {width,height});
		assert.deepEqual(errors, []);
		console.log(
			width + '×' + height + ' six views, fail-open preference and four status transitions, 10 minutes of stable overview polling, dialogs, URL/TCP probes, kernel install, notices that outlive their page and isolated clear passed',
		);
		await page.close();
	}
	await checkMiddleWidth(browser, base);
	console.log('800×900 two subscription cards a row, stacked rule columns and two rule-set cards a row passed');
	await checkDarkOverview(browser, base);
	console.log('1280×900 dark overview: the power button\'s dark red and legible status text passed');
} finally {
	await browser.close();
	await new Promise((resolve) => server.close(resolve));
}
