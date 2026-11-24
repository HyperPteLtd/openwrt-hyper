import { t } from './i18n.js';
import { h, card, knownNames, pages, feedback, feedbackFor, say, hush, diagnosticMessage, nameMembers } from './dom.js';
import { overview } from './pages/overview.js';
import { proxy } from './pages/proxy.js';
import { hyperAccount } from './pages/account.js';
import { plans } from './pages/plans.js';
import { rules } from './pages/rules.js';
import { records } from './pages/records.js';

const views = {
	overview: [t('概览'), overview],
	proxy: [t('代理'), proxy],
	account: [t('账户'), hyperAccount],
	rules: [t('规则'), rules],
	logs: [t('日志'), records],
	connections: [t('连接记录'), records],
	// Reached from the account page, not the navigation.
	plans: [t('套餐'), plans],
};
const hidden = new Set(['plans']);
const aliases = {
	nodes: 'proxy',
	groups: 'proxy',
	subscriptions: 'account',
	subs: 'account',
	login: 'account',
	devices: 'overview',
	settings: 'overview',
	events: 'logs',
};
function route() {
	const [key, raw = ''] = location.hash.slice(1).split('/');
	let detail = '';
	try {
		detail = decodeURIComponent(raw);
	} catch (_) {}
	const page = aliases[key] || key;
	return { page: Object.hasOwn(views, page) ? page : 'overview', detail };
}
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
export function mount(root, invoke) {
	root.className = 'ow';
	const nav = h('nav', { 'aria-label': 'Hyper' });
	const main = h('div', { class: 'ow-main' });
	const alert = h('div', { class: 'ow-alert', role: 'status', hidden: true });
	const header = h(
		'div',
		{ class: 'ow-header' },
		h(
			'div',
			{ class: 'ow-brand' },
			h('img', { src: new URL('mark.svg', import.meta.url).href, alt: '', width: 36, height: 36 }),
			h('h1', {}, 'Hyper'),
		),
		nav,
	);
	for (const [page, [name]] of Object.entries(views)) if (!hidden.has(page)) nav.append(h('a', { href: '#' + page }, name));
	root.replaceChildren(header, alert, main);
	let scope,
		disconnected = false,
		noticeTimer,
		onceMounted = root.isConnected,
		// The page main shows, so a redraw of it can keep the focus where it was.
		drawn = '';
	const memory = {};
	// The mounted page's feedback lines by key, for outcomes reported after it re-rendered.
	let keyed = new Map();
	// How long the alert shows a notice, and a keyed line keeps one across re-renders.
	const linger = 8000;
	let names = new Map(),
		catalogKey = '',
		catalogRequest;
	const name = (id) => names.get(id) || knownNames[id] || id || '—';
	// The service's errors name a node or group by its ID; they read with its name once known.
	nameMembers((id) => names.get(id) || '');
	// Starting or stopping the proxy reloads the firewall, DNS and routes, which can drop the
	// browser's open requests ("XHR request aborted by browser"). Reads are retried briefly.
	const transient = (e) => /aborted|timed? ?out|network|xhr/i.test(e?.message || '');
	const rereadable = new Set(['task', 'status', 'routes', 'traffic', 'groups', 'live_groups', 'latencies', 'logs',
		'connections', 'environment', 'devices', 'custom_rules', 'network_preferences', 'plugin_check',
		'subscription_read', 'kernel_read', 'conflicts']);
	const call = async (method, args) => {
		for (let attempt = 1; ; attempt++) {
			try {
				return await invoke(method, args);
			} catch (e) {
				if (!transient(e) || !rereadable.has(method) || attempt >= 8 || disconnected) throw e;
				await delay(Math.min(4000, 500 * attempt));
			}
		}
	};
	const rpc = async (method, args = {}) => {
		const v = await call(method, args);
		// uhttpd encodes a successful empty ubus table as just status 0.
		if ((v === 0 || v == null) && (method === 'groups' || method === 'latencies')) return {};
		if (!v || v.ok === false || (v.error && !('saved' in v || 'stage' in v)))
			throw new Error(v?.error || t('路由器未返回结果'));
		return v;
	};
	const notify = (message) => {
		if (disconnected) return;
		const open = [...root.querySelectorAll('dialog[open]')].pop();
		const target = open?.querySelector('.ow-modal-status') || alert;
		target.textContent = String(message);
		target.hidden = false;
		if (target !== alert) return;
		clearTimeout(noticeTimer);
		noticeTimer = setTimeout(() => {
			alert.hidden = true;
		}, linger);
	};
	// What a keyed line shows outlives a re-render of its page, such as the overview's after the
	// service changed phase, for as long as the alert would show it: the line registered under the
	// same key takes it over, unless a retry or a newer outcome replaced it before.
	const told = new Map();
	function keep(line, text, tone) {
		for (const [key, held] of keyed) if (held === line) told.set(key, { line, text, tone, until: Date.now() + linger });
	}
	function retell() {
		for (const [key, note] of told) {
			const line = keyed.get(key);
			if (note.until < Date.now() || note.line.textContent !== note.text) told.delete(key);
			else if (line && line !== note.line) {
				say(line, note.text, { tone: note.tone });
				note.line = line;
			}
		}
	}
	// An action's error goes to the line of the row or card its control sits in; the alert at the
	// top keeps what has no such place. An error the page already wrote beside its control (shown
	// names the element) is not repeated while that element is on screen: still on the page, and
	// in the control's dialog when there is one, as the page behind a dialog is out of view.
	// The router's error codes read in Chinese, as the service's problem does under the power button.
	function report(control, error) {
		const shown = error?.shown,
			open = control?.closest('dialog[open]');
		if (shown?.isConnected && (!open || open.contains(shown))) return;
		const message = diagnosticMessage(error?.message ?? String(error)),
			line = control && feedbackFor(control);
		if (line?.isConnected) {
			say(line, message, { control });
			keep(line, message, 'error');
		} else notify(message);
	}
	// After an action re-rendered the page, its outcome goes to the line now registered under the
	// first key the page has; a key registered as covered means the page already shows it.
	function tell(keys, text, tone = 'error') {
		for (const key of [keys].flat()) {
			if (!keyed.has(key)) continue;
			const line = keyed.get(key);
			if (!line) return;
			if (line.isConnected) {
				say(line, text, { tone });
				return keep(line, text, tone);
			}
		}
		notify(text);
	}
	function button(text, handler, cls = '') {
		const el = h('button', { type: 'button', class: cls }, text);
		el.addEventListener('click', async (ev) => {
			// A control the view marks aria-disabled stays focusable and ignores presses, as it
			// does while its own action runs.
			if (el.disabled || el.hasAttribute('aria-busy') || el.getAttribute('aria-disabled') === 'true') {
				ev.preventDefault();
				return;
			}
			// A new attempt clears what the last one reported. A line about a blocking condition
			// is cleared by the page once it actually retries; a dialog's status line is cleared by
			// any of its buttons.
			const line = feedbackFor(el);
			if (line && !line.contains(el) && !line.classList.contains('ow-blocking')) hush(line);
			const status = el.closest('dialog')?.querySelector('.ow-modal-status');
			if (status) {
				status.hidden = true;
				status.textContent = '';
			}
			// Keep the trigger focusable so native dialogs can restore focus after
			// an asynchronous action. The view owns the actual disabled state.
			el.setAttribute('aria-busy', 'true');
			el.setAttribute('aria-disabled', 'true');
			try {
				await handler(ev);
			} catch (e) {
				if (e.name !== 'AbortError') report(el, e);
			} finally {
				el.removeAttribute('aria-busy');
				el.removeAttribute('aria-disabled');
			}
		});
		return el;
	}
	// A redraw drops the focused control with the content it replaces. The control drawn in its
	// place under the same data-focus-key takes the focus back, unless focus moved on meanwhile.
	function refocus(scope, key) {
		const active = document.activeElement;
		if (!key || (active && active !== document.body) || root.querySelector('dialog[open]')) return;
		scope.querySelector('[data-focus-key="' + CSS.escape(key) + '"]')?.focus({ preventScroll: true });
	}
	function dialog(title, body) {
		// Closed, a dialog returns focus to the control that opened it, or, when a redraw meanwhile
		// replaced that control, to the one drawn in its place.
		const opener = document.activeElement,
			openerKey = opener?.dataset?.focusKey;
		const heading = h('h2', {}, title);
		const d = h(
			'dialog',
			{ class: 'ow-modal', 'aria-label': title },
			h(
				'div',
				{ class: 'ow-modal-head' },
				heading,
				button('×', () => d.close(), 'ow-close'),
			),
			h('div', { class: 'ow-modal-body' }, h('p', { class: 'ow-modal-status', role: 'alert', hidden: true }), body),
		);
		d.querySelector('.ow-close').setAttribute('aria-label', t('关闭'));
		root.append(d);
		d.addEventListener('close', () => {
			d.remove();
			if (openerKey && !opener.isConnected) refocus(main, openerKey);
		}, { once: true });
		d.showModal();
		// A press on the backdrop closes the dialog: one that starts and ends outside it, not a
		// selection dragged out of it, and not the second click of a double click that opened it.
		const opened = performance.now(),
			outside = (e) => {
				if (e.target !== d) return false;
				const r = d.getBoundingClientRect();
				return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
			};
		let pressed = false;
		d.addEventListener('pointerdown', (e) => {
			pressed = outside(e) && performance.now() - opened > 400;
		});
		d.addEventListener('click', (e) => {
			if (pressed && outside(e)) d.close();
			pressed = false;
		});
		return d;
	}
	function go(page, detail = '') {
		const dest = '#' + page + (detail ? '/' + encodeURIComponent(detail) : '');
		if (location.hash === dest) render();
		else location.hash = dest;
	}
	const storageKey = 'hyper.pending.' + location.pathname;
	function track(ids) {
		try {
			sessionStorage.setItem(storageKey, JSON.stringify(ids.slice(-32)));
		} catch (_) {}
	}
	function pending() {
		try {
			return JSON.parse(sessionStorage.getItem(storageKey)) || [];
		} catch (_) {
			return [];
		}
	}
	async function run(method, args = {}, onAccepted) {
		let accepted;
		try {
			accepted = await rpc(method, args);
		} catch (e) {
			// The router may already be applying the change when the connection drops; the
			// next status read shows what actually happened.
			if (!transient(e) || !['start', 'stop', 'mode'].includes(method)) throw e;
			await delay(1500);
			return {};
		}
		onAccepted?.();
		if (!accepted.task || accepted.task.startsWith('intent-')) return accepted;
		const id = accepted.task;
		track([...new Set([...pending(), id])]);
		for (let pause = 250; !disconnected; pause = Math.min(1600, pause + 150)) {
			await delay(pause);
			const info = await rpc('task', { task: id });
			if (info.stage === 'running') continue;
			track(pending().filter((x) => x !== id));
			if (info.stage !== 'done') {
				const failure = new Error(info.result?.error || info.error || t('任务未完成'));
				if (method === 'subscription_write') {
					failure.provider = info.result?.provider;
					failure.tls = info.result?.tls;
				}
				throw failure;
			}
			return info.result?.ok === true ? info.result.data : info.result;
		}
		throw new DOMException(t('已离开页面'), 'AbortError');
	}
	async function render() {
		scope?.abort();
		const controller = new AbortController();
		scope = controller;
		const { page, detail } = route();
		const tab = page === 'plans' ? 'account' : page;
		for (const a of nav.children) {
			if (a.hash === '#' + tab) a.setAttribute('aria-current', 'page');
			else a.removeAttribute('aria-current');
		}
		main.setAttribute('aria-busy', 'true');
		try {
			const [status, sourceList] = await Promise.all([
				rpc('status'),
				rpc('subscription_read', { action: 'list', args: {} }),
			]);
			if (controller.signal.aborted) return;
			status.intent = status.saved;
			const sources = sourceList.data || [];
			const lines = new Map();
			const ui = {
				h,
				card,
				rpc,
				run,
				go,
				dialog,
				report,
				tell,
				// A feedback line for the controls inside scope, optionally under a key for tell().
				// An assertive line reports a blocking condition: its control's next press leaves
				// it to the page, which clears it once it actually retries.
				feedback: (scope, key, { assertive = false, covered = false } = {}) => {
					const line = feedback(scope, assertive);
					if (assertive) line.classList.add('ow-blocking');
					if (key) lines.set(key, covered ? null : line);
					return line;
				},
				name,
				button,
				memory,
				page,
				detail,
				status,
				sources,
				signal: controller.signal,
				refresh: render,
			};
			let kernelState;
			ui.kernelStatus = () => kernelState ||= rpc('kernel_read', { action: 'status' });
			ui.catalog = async () => {
				const source = status.intent.source;
				const selectedKernel = (await ui.kernelStatus()).selected || '';
				const key = JSON.stringify([
					selectedKernel, source,
					sources.map((x) => [x.source, x.generation, x.epoch]),
					status.intent.policy?.groups,
				]);
				if (catalogKey !== key) {
					catalogKey = key;
					names = new Map();
					const primary = sources.find((s) => s.source === source);
					catalogRequest = Promise.all(
						primary?.ready ? [pages({ rpc }, source, 'nodes'), pages({ rpc }, source, 'groups')] : [[], []],
					)
						.then(([nodes, groups]) => {
							const local = (status.intent.policy?.groups || []).map((g) => ({
								...g,
								kind: 'selector',
								members: g.members.map((m) => m.node),
							}));
							const g = [...groups, ...local];
							if (primary?.local)
								g.unshift({
									id: '@default',
									name: t('默认代理'),
									kind: 'selector',
									members: nodes.filter((n) => !n.special).map((n) => n.id),
								});
							if (catalogKey === key) names = new Map([...nodes, ...g].map((x) => [x.id, x.name]));
							return { nodes, groups: g, primary };
						})
						.catch((e) => {
							if (catalogKey === key) catalogKey = '';
							throw e;
						});
				}
				return catalogRequest;
			};
			ui.poll = (callback, interval = 3500) => {
				let timer;
				const tick = async () => {
					if (controller.signal.aborted) return;
					if (!document.hidden) {
						try {
							await callback();
						} catch (e) {
							if (!controller.signal.aborted) notify(diagnosticMessage(e.message));
						}
					}
					if (!controller.signal.aborted) timer = setTimeout(tick, interval);
				};
				timer = setTimeout(tick, interval);
				controller.signal.addEventListener('abort', () => clearTimeout(timer), { once: true });
			};
			const content = await views[page][1](ui);
			if (controller.signal.aborted) return;
			const focused = document.activeElement,
				focusKey = drawn === page && main.contains(focused) ? focused.dataset.focusKey : '';
			// Kept outcomes are in their new lines before these are shown: they are not news, so a
			// screen reader does not read them out again at every redraw.
			keyed = lines;
			retell();
			main.replaceChildren(content);
			drawn = page;
			refocus(content, focusKey);
			ui.afterRender?.();
		} catch (e) {
			if (!controller.signal.aborted) {
				keyed = new Map();
				drawn = '';
				main.replaceChildren(card(t('无法加载页面'), h('p', {}, e.message), button(t('重试'), render)));
			}
		} finally {
			if (!controller.signal.aborted) main.setAttribute('aria-busy', 'false');
		}
	}
	function changePage() {
		for (const d of root.querySelectorAll('dialog[open]')) d.close();
		render();
	}
	window.addEventListener('hashchange', changePage);
	const observer = new MutationObserver(() => {
		if (root.isConnected && !onceMounted) {
			onceMounted = true;
			render();
		}
		else if (!root.isConnected && onceMounted) dispose();
	});
	observer.observe(document.documentElement, { childList: true, subtree: true });
	function dispose() {
		disconnected = true;
		scope?.abort();
		clearTimeout(noticeTimer);
		observer.disconnect();
		window.removeEventListener('hashchange', changePage);
	}
	if (onceMounted) render();
	async function restoreTasks() {
		for (const id of pending()) {
			try {
				const task = await rpc('task', { task: id });
				if (task.stage !== 'running') {
					track(pending().filter((x) => x !== id));
					if (task.stage !== 'done') notify(diagnosticMessage(task.error) || t('上次任务未完成'));
					else render();
				}
			} catch (_) {
				track(pending().filter((x) => x !== id));
			}
		}
		if (!disconnected && pending().length) setTimeout(restoreTasks, 2000);
	}
	if (pending().length) restoreTasks();
	return dispose;
}
