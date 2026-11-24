import { t, languagePreference, setLanguagePreference } from '../i18n.js';
import { diagnosticMessage, day, pluginName, conflictItem } from '../dom.js';
import { h, card, bytes, field, empty, segment, modeNames, clock, resultName, say, hush, attach } from '../dom.js';
import { installBundled, kernelStorage, kernelSummary } from './versions.js';

const states = {
	running: t('已连接'),
	direct: t('未连接'),
	initializing: t('正在准备'),
	starting: t('连接中'),
	releasing: t('断开中'),
	release_failed: t('正在恢复网络'),
	fail_open: t('已回退直连'),
	protected: t('故障保护中'),
	state_unavailable: t('状态待修复'),
};
const phaseDetail = {
	initializing: t('正在读取配置'),
	starting: t('正在启动代理服务'),
	releasing: t('正在释放网络规则'),
	direct: t('全屋流量直连'),
	fail_open: t('代理故障，局域网已恢复直连'),
	protected: t('代理流量已暂停，等待内核恢复'),
	release_failed: t('正在检查网络恢复'),
};
const svgNS = 'http://www.w3.org/2000/svg';
function shape(tag, attrs) {
	const el = document.createElementNS(svgNS, tag);
	for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
	return el;
}
function chart(samples) {
	const svg = shape('svg', {
		viewBox: '0 0 300 75',
		preserveAspectRatio: 'none',
		role: 'img',
		'aria-label': t('近 1 分钟上下行速率'),
	});
	svg.append(shape('path', { d: 'M0 38H300 M0 74H300', class: 'ow-gridline' }));
	const end = Date.now(),
		points = samples.filter((s) => end - s.t <= 60000);
	if (!points.length) return svg;
	const max = Math.max(1, ...points.flatMap((s) => [s.down_bytes_per_second, s.up_bytes_per_second]));
	for (const [key, color] of [
		['down_bytes_per_second', 'var(--ow-teal)'],
		['up_bytes_per_second', 'var(--ow-violet)'],
	]) {
		const line = points.map((s) => [Math.max(0, 300 - (end - s.t) / 200), 72 - (s[key] * 66) / max]);
		if (line.length === 1) {
			svg.append(shape('circle', { cx: line[0][0], cy: line[0][1], r: 2, fill: color }));
			continue;
		}
		const d = line.map(([x, y], i) => (i ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1)).join(' ');
		if (key === 'down_bytes_per_second')
			svg.append(
				shape('path', {
					d: d + ' L' + line.at(-1)[0] + ' 75 L' + line[0][0] + ' 75 Z',
					fill: 'var(--ow-tint)',
				}),
			);
		svg.append(
			shape('path', {
				d,
				fill: 'none',
				stroke: color,
				'stroke-width': 2,
				'vector-effect': 'non-scaling-stroke',
			}),
		);
	}
	return svg;
}
function uptime(ms) {
	const s = Math.max(0, Math.floor(ms / 1000)),
		pad = (v) => String(v).padStart(2, '0');
	return (
		(s >= 86400 ? t('{0} 天 ', Math.floor(s / 86400)) : '') +
		[Math.floor(s / 3600) % 24, Math.floor(s / 60) % 60, s % 60].map(pad).join(':')
	);
}
const networkRequirements = new Map([
	['unsupported capture method', t('当前接管方式不受支持')],
	['both fw3 and fw4 are active', t('fw3 和 fw4 同时运行，请只启用其中一个')],
	['no active fw3 or fw4 firewall', t('请先启用 fw3 或 fw4 防火墙')],
	['numeric policy routing and marked route lookup', t('ip 命令需要支持数字路由表和按流量标记查询路由')],
	['dnsmasq service with configurable upstream', t('dnsmasq 服务需要支持设置上游 DNS')],
	['DNS lookup command', t('缺少用于检查 DNS 解析的命令')],
	['JSON inspection of the active fw4 ruleset', t('无法以 JSON 格式读取当前 fw4 规则')],
	['fw4 policy marking and destination lookup', t('fw4 无法标记流量或查询目标地址')],
	['fw4 TProxy target', t('fw4 缺少 TProxy 透明代理支持')],
	['fw4 DNS redirection', t('fw4 无法重定向 DNS 请求')],
	['legacy IPv4 rule inspection and restore', t('无法读取或恢复传统 iptables 的 IPv4 规则')],
	['legacy IPv6 rule inspection and restore', t('无法读取或恢复传统 iptables 的 IPv6 规则')],
	['active fw3 IPv4 chains', t('fw3 的 IPv4 规则链尚未启用')],
	['active fw3 IPv6 chains', t('fw3 的 IPv6 规则链尚未启用')],
	['fw3 IPv4 DNS redirection', t('fw3 无法重定向 IPv4 DNS 请求')],
	['fw3 IPv6 DNS redirection', t('fw3 无法重定向 IPv6 DNS 请求')],
	['fw3 destination address type match', t('fw3 无法匹配目标地址类型')],
	['fw3 TProxy target', t('fw3 缺少 TProxy 透明代理支持')],
	['fw3 filter and policy marking rules', t('fw3 无法过滤或标记流量')],
	['usable /dev/net/tun device', t('TUN 网络设备不可用（/dev/net/tun）')],
]);
function firewallDetails(env) {
	const names = { fw4: 'fw4 / nftables', fw3: 'fw3 / iptables-legacy', ambiguous: t('检测到两套防火墙正在运行'), unavailable: t('未检测到启用的防火墙') };
	const backend = env ? names[env.firewall_backend] || (env.firewall4 ? names.fw4 : names.unavailable) : t('暂时无法检查');
	const fields = [[t('防火墙'), [backend, env?.firewall_version].filter(Boolean).join(' · ')]];
	if (env?.firewall_backend === 'fw3' || env?.firewall_backend === 'fw4') {
		for (const [key, name] of [['firewall_tproxy', 'TProxy'], ['firewall_tun', 'TUN']]) {
			if (typeof env[key] === 'boolean') fields.push([name, env[key] ? t('可用') : t('缺少运行条件')]);
		}
	}
	if (env?.dns_issue) fields.push([t('DNS 接管'), diagnosticMessage(env.dns_issue)]);
	if (env?.network_requirements?.length) {
		fields.push([t('需要处理'), env.network_requirements.map((item, i) => [i > 0 && h('br'), networkRequirements.get(item) || item])]);
		if (env.firewall_packages?.length) fields.push([t('可提供所缺功能的软件包'), env.firewall_packages.join(t('、'))]);
	}
	return fields;
}
// Starting the proxy stops and disables other proxy plugins; only the rest needs a hand.
function conflictSummary(found) {
	if (!found) return t('暂时无法检查');
	if (!found.blocked) return t('未检测到');
	const resources = found.resources || [];
	const plugins = [...new Set(resources.filter((x) => x.service).map((x) => pluginName(x.service)))];
	const manual = [...new Set(resources.filter((x) => !x.service).map((x) => conflictItem(x.kind, x.name)))];
	return [plugins.length && t('{0} · 启动时自动停用', plugins.join(t('、'))), manual.length && t('需手动处理：{0}', manual.join(t('、')))]
		.filter(Boolean).join(t('；')) || t('暂时无法检查');
}
function environmentRows(fields) {
	return h('div', { class: 'ow-environment' },
		fields.map(([key, value]) => h('div', {}, h('span', {}, key), h('strong', {}, value))));
}
function sourceCard(ui, setup) {
	const current = ui.status.intent.source;
	const sources = [...ui.sources.filter((s) => !s.revoked)].sort(
		(a, b) => Number(b.source === current) - Number(a.source === current),
	);
	const box = card(
		'',
		h(
			'div',
			{ class: 'ow-row' },
			h('h2', {}, t('Hyper 账户')),
			ui.button(t('管理'), () => ui.go('account'), 'ow-link'),
		),
	);
	box.classList.add('ow-sources');
	// Switching fails here; a subscription added from the setup reports its import here too.
	box.append(ui.feedback(box, 'sources'));
	// Each subscription is listed twice, for the phone's menu and the wide list.
	function item(s, list) {
		const active = s.source === current,
			p = s.usage || s.plan;
		const b = ui.button(
			'',
			async () => {
				if (active || !s.ready) ui.go('account');
				else {
					await ui.run('source', { source: s.source, revision: ui.status.intent.revision });
					await ui.refresh();
				}
			},
			'ow-source-line' + (active ? ' is-active' : ''),
		);
		b.setAttribute('aria-label', (active ? t('管理') : t('切换至')) + ' ' + (s.name || s.source));
		b.dataset.focusKey = 'source:' + list + ':' + s.source;
		const tail = h(
			'div',
			{ class: 'ow-source-sub' },
			p?.total > 0
				? h('span', {}, bytes(p.upload + p.download) + ' / ' + bytes(p.total))
				: h('span', {}, t('{0} 个节点', s.node_count || 0)),
			p?.expireAt ? h('span', {}, t('到期 {0}', day(p.expireAt))) : null,
		);
		const content = h(
			'div',
			{ class: 'ow-source-info' },
			h('strong', {}, s.name || s.source),
			p?.total > 0 && h('progress', { max: p.total, value: Math.min(p.total, p.upload + p.download) }),
			tail,
		);
		b.append(
			h('span', { class: 'ow-avatar' }, Array.from(s.name || t('订阅'))[0]),
			content,
			h('small', {}, active ? t('当前订阅') : s.ready ? t('切换') : t('管理')),
		);
		return b;
	}
	if (sources.length) {
		const primary = sources.find(s => s.source === current);
		const title = primary?.name || t('选择订阅');
		const link = ui.button(t('账户'), () => ui.go('account'), 'ow-link');
		box.append(h('details', { class: 'ow-source-mobile' },
			h('summary', { 'aria-label': t('切换订阅') },
				h('span', { class: 'ow-avatar', 'aria-hidden': 'true' }, Array.from(title)[0]),
				h('span', { class: 'ow-source-info' }, h('strong', {}, title), h('small', {}, t('切换订阅'))),
			),
			h('div', { class: 'ow-stack' }, ...sources.map((s) => item(s, 'menu')), link),
		));
	}
	// Without a subscription the card offers the setup that adds the first one.
	if (!sources.length)
		box.append(setup
			? h('div', { class: 'ow-source-setup' }, h('p', {}, t('添加第一个订阅，开始配置全屋代理。')), setup)
			: empty(t('尚未添加订阅')));
	if (sources.length) box.append(h('div', { class: 'ow-source-list' }, ...sources.map((s) => item(s, 'list'))));
	return box;
}
function deviceCard(ui, devices) {
	const policy = ui.status.intent.policy || {},
		section = card('');
	section.classList.add('ow-devices');
	let filter = 'all';
	const query = h('input', { type: 'search', placeholder: t('搜索名称 / IP / MAC'), 'aria-label': t('搜索设备') }),
		list = h('div', {});
	// A device's line follows its row through searches and filters.
	const lines = new Map();
	const reported = (row, mac) => {
		if (!lines.has(mac)) lines.set(mac, ui.feedback());
		return [row, attach(row, lines.get(mac))];
	};
	const online = devices.filter((d) => d.reachability === 'reachable').length;
	const filters = h('div', { class: 'ow-segment', 'aria-label': t('设备筛选'), role: 'group' });
	function policyOf(d) {
		return policy.devices?.find((r) => r.address.toLowerCase() === d.mac.toLowerCase())?.target || '';
	}
	// Each row shows the effective choice; picking what new devices get anyway drops the override.
	const effective = (d) => policyOf(d) || (policy.default_direct ? '@direct' : '@proxy');
	async function choose(d, target) {
		if (target === (policy.default_direct ? '@direct' : '@proxy')) target = '';
		const all = (policy.devices || []).filter((x) => x.address.toLowerCase() !== d.mac.toLowerCase());
		if (target) all.push({ address: d.mac, target });
		await ui.run('policy', { revision: ui.status.intent.revision, policy: { ...policy, devices: all } });
		await ui.refresh();
	}
	function draw() {
		const q = query.value.toLowerCase(),
			visible = devices.filter((d) => {
				const explicit = policyOf(d),
					direct = explicit === '@direct' || (!explicit && policy.default_direct);
				return (
					(!q || [d.name, d.mac, ...d.addresses].join(' ').toLowerCase().includes(q)) &&
					(filter === 'all' ||
						(filter === 'online' && d.reachability === 'reachable') ||
						(filter === 'direct' && direct) ||
						(filter === 'proxy' && !direct))
				);
			});
		for (const b of filters.children) b.setAttribute('aria-pressed', String(b.dataset.value === filter));
		list.replaceChildren(
			...visible.flatMap((d) => reported(
				h(
					'div',
					{ class: 'ow-device-row' },
					h('span', { class: 'ow-dot' + (d.reachability === 'reachable' ? ' good' : '') }),
					h(
						'div',
						{ class: 'ow-device-name' },
						h('strong', {}, d.name || d.mac),
						h('small', {}, d.addresses.join(' · ')),
					),
					focusKeys(segment(
						ui,
						t('代理方式'),
						[
							['@proxy', t('代理')],
							['@direct', t('直连')],
						],
						effective(d),
						(v) => choose(d, v),
					), 'device:' + d.mac),
				),
				d.mac,
			)),
		);
		if (!visible.length) list.append(empty(t('暂无匹配设备')));
	}
	for (const [id, text] of [
		['all', t('全部')],
		['online', t('在线')],
		['proxy', t('代理')],
		['direct', t('直连')],
	]) {
		const b = ui.button(text, () => {
			filter = id;
			draw();
		});
		b.dataset.value = id;
		filters.append(b);
	}
	query.addEventListener('input', draw);
	draw();
	section.append(
		h(
			'div',
			{ class: 'ow-row' },
			h('h2', {}, t('设备')),
			h('small', { class: 'ow-muted' }, t('在线 {0} / {1}', online, devices.length)),
		),
		h('div', { class: 'ow-toolbar ow-device-toolbar' }, filters, query),
		list,
	);
	return section;
}
// The power button. Off it is the theme colour. While the proxy connects or disconnects it keeps
// the theme colour and its ring pulses; connected, and in every other phase the proxy stays on in
// (直连 mode, a fault, a failed release), the whole button is the red disconnect control, still.
function powerLook({ intent, phase }) {
	if (phase === 'releasing') return 'disconnecting';
	// Until the router takes up a new intent it still reports the phase of the old one.
	if (!intent.enabled)
		return ['running', 'starting', 'protected', 'release_failed'].includes(phase) ? 'disconnecting' : 'off';
	if (phase === 'initializing' || phase === 'starting' || (phase === 'direct' && intent.mode !== 'direct'))
		return 'connecting';
	return 'on';
}
// The word on the button and its accessible name. Off, the word says what a press does.
function powerWords(look, off) {
	return {
		connecting: [t('连接中'), t('连接中，点按断开')],
		on: [t('断开'), t('断开')],
		disconnecting: [t('断开中'), t('断开中')],
	}[look] || [off, off];
}
const pulsing = (look) => look === 'connecting' || look === 'disconnecting';
// CSS animations run on the document timeline, so a pulse's start is kept on it too.
const timeline = () => document.timeline?.currentTime ?? performance.now();
const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const worn = new WeakMap(),
	fades = new WeakMap();
// Puts a look's classes on the button. A pulse that starts here begins `since` into its cycle,
// through a negative delay; a pulse already running keeps its timing.
function dress(el, look, since) {
	const was = worn.get(el);
	el.classList.toggle('on', look === 'on');
	el.classList.toggle('pulse', pulsing(look));
	el.classList.toggle('gentle', look === 'disconnecting');
	if (was?.look !== look || was?.since !== since)
		el.style.animationDelay = pulsing(look) ? Math.min(0, since - timeline()) + 'ms' : '';
	worn.set(el, { look, since });
}
const fade = (el, from, to, fill) => {
	try {
		return el.animate({ opacity: [from, to] }, { duration: 320, easing: 'ease', fill });
	} catch (_) {
		return null;
	}
};
// Shows a look on the power button. `before` ({look, since, text}) is what was on screen until
// now: this button's last look, or what the button a redraw replaces showed, which a new button
// puts on for a moment to measure. With motion the fill, text colour and ring ease over from it
// and the word fades across. A pulse that carries on keeps its phase either way.
function paintPower(el, look, [text, name], before, motion) {
	const since = pulsing(look) ? (before?.look === look ? before.since : timeline()) : null;
	let from;
	if (motion && before && before.look !== look) {
		el.style.transition = 'none';
		dress(el, before.look, before.since);
		const style = getComputedStyle(el);
		from = { offset: 0, backgroundColor: style.backgroundColor, color: style.color, boxShadow: style.boxShadow };
	}
	dress(el, look, since);
	if (from) {
		getComputedStyle(el).color; // the new look applies while transitions are off
		el.style.removeProperty('transition');
		fades.get(el)?.cancel();
		try {
			// One keyframe: the animation ends on whatever the look shows, a running pulse included.
			fades.set(el, el.animate([from], { duration: 420, easing: 'ease' }));
		} catch (_) {} // without partial keyframes the look simply switches
	}
	const word = el.querySelector('.ow-power-label').lastElementChild;
	if (motion && before && before.text !== text) {
		const old = h('span', { 'aria-hidden': 'true' }, before.text);
		word.before(old);
		const out = fade(old, 1, 0, 'forwards');
		fade(word, 0, 1);
		if (out) out.onfinish = out.oncancel = () => old.remove();
		else old.remove();
	}
	word.textContent = text;
	el.setAttribute('aria-label', name);
	// Disconnecting, a press would do nothing until the router lets go: the button says so and
	// stays in the tab order. While its own press runs, the press keeps it marked (app.js button).
	if (look === 'disconnecting') el.setAttribute('aria-disabled', 'true');
	else if (!el.hasAttribute('aria-busy')) el.removeAttribute('aria-disabled');
	return { look, since, text };
}
// A redraw gives the focus back to the control drawn under the same key (app.js render).
function focusKeys(group, prefix) {
	for (const b of group.querySelectorAll('button')) b.dataset.focusKey = prefix + ':' + b.textContent;
	return group;
}
// Runs fn on the first frame el is in the page, before that frame is painted. The wait ends when
// a newer render replaces this one, or after about 20 seconds of frames if the render failed.
function onFirstFrame(el, signal, fn) {
	let frames = 1200;
	const frame = () => {
		if (el.isConnected) fn();
		else if (!signal.aborted && --frames > 0) requestAnimationFrame(frame);
	};
	requestAnimationFrame(frame);
}
export async function overview(ui) {
	const intent = ui.status.intent,
		running = ui.status.phase === 'running';
	const [prefs, devices, initialKernel, cat, routes] = await Promise.all([
		ui.rpc('network_preferences'),
		ui.rpc('devices').catch(() => ({unavailable:true,items:[]})),
		ui.kernelStatus(),
		ui.catalog(),
		ui.rpc('routes', { limit: 12 }),
	]);
	let kernel = initialKernel, nextKernelPoll = 0;
	const kernelUpdates = new EventTarget();
	kernelUpdates.addEventListener('change', (event) => { kernel = event.detail; }, {signal:ui.signal});
	if (!kernel.selected || kernel.bootstrap?.stage === 'running') ui.poll(async () => {
		if ((kernel.selected && kernel.bootstrap?.stage !== 'running') || Date.now() < nextKernelPoll) return;
		const previous = kernel;
		nextKernelPoll = Date.now() + (kernel.bootstrap?.stage === 'running' ? 3500 : 30000);
		const next = await ui.rpc('kernel_read', { action: 'status' });
		if (!ui.signal.aborted && previous === kernel)
			kernelUpdates.dispatchEvent(new CustomEvent('change', {detail:next}));
	}, 3500);
	const policy = intent.policy || {};
	const saveP = async (changes) => {
		await ui.run('save_network', { args: { ...prefs, ...changes } });
		await ui.refresh();
	};
	const savePolicy = async (changes) => {
		await ui.run('policy', { revision: intent.revision, policy: { ...policy, ...changes } });
		await ui.refresh();
	};
	const header = h('h2', {}, states[ui.status.phase] || ui.status.phase),
		sub = h('span', { class: 'ow-muted' });
	// Off, the word says what a press does first; while a press downloads the kernel it says so.
	let preparing = false;
	const offWord = () =>
		!cat.primary
			? t('先添加订阅')
			: kernel.selected
				? t('连接')
				: preparing || kernel.bootstrap?.stage === 'running' ? t('内核准备中') : t('先安装内核');
	let look = powerLook(ui.status),
		shown;
	// What the next button needs to carry on from this one: its look, word and pulse timing, and
	// whether it is still on screen. The button itself is held weakly, so a page switch frees it.
	const remember = () => {
		ui.memory.power = { ...shown, el: new WeakRef(power) };
	};
	// Changes the button where it stands: at once on a press, and back if the router refuses.
	const show = (next) => {
		if (!power.isConnected) return;
		look = next;
		shown = paintPower(power, next, powerWords(next, offWord()), shown, !reducedMotion());
		remember();
	};
	const start = async () => {
		// Disconnecting: a press waits for the router to let go (the button is aria-disabled).
		if (look === 'disconnecting') return;
		if (look !== 'off') {
			const d = ui.dialog(
				t('断开代理'),
				h(
					'div',
					{ class: 'ow-stack' },
					h('p', {}, t('断开后全屋设备将直连。')),
					ui.button(
						t('断开'),
						async () => {
							const before = look;
							show('disconnecting');
							try {
								// An open dialog holds the page's redraws back, so a change saved
								// meanwhile is not in the page: the stop names the revision now current.
								const { saved } = await ui.rpc('status');
								await ui.run('stop', { revision: saved.revision });
							} catch (error) {
								show(before);
								throw error;
							}
							d.close();
							await ui.refresh();
						},
						'ow-danger',
					),
				),
			);
			return;
		}
		if (!cat.primary) {
			ui.go('account');
			return;
		}
		// Connecting again: what stopped the last attempt no longer applies, and what stops this
		// one is news.
		hush(powerLine);
		powerLine.setAttribute('role', 'alert');
		if (!kernel.selected) {
			// Connecting needs a kernel: install the one the plugin carries first.
			preparing = true;
			show('off');
			try {
				kernel = await installBundled(ui, kernelUpdates, kernel);
			} catch (error) {
				preparing = false;
				show('off');
				if (error.name === 'AbortError') return;
				throw error;
			}
			preparing = false;
		}
		// Connecting shows at once; the refresh confirms it, a refusal rolls it back.
		show('connecting');
		try {
			await ui.run('start', { revision: intent.revision });
		} catch (error) {
			show('off');
			throw error;
		}
		await ui.refresh();
	};
	const power = ui.button('', start, 'ow-power');
	power.dataset.focusKey = 'power';
	const icon = shape('svg', { viewBox: '0 0 24 24', width: 38, height: 38, 'aria-hidden': 'true' });
	icon.append(
		shape('path', {
			d: 'M12 3v9M6.6 6.8a7.5 7.5 0 1 0 10.8 0',
			fill: 'none',
			stroke: 'currentColor',
			'stroke-width': 2.3,
			'stroke-linecap': 'round',
		}),
	);
	power.append(icon, h('span', { class: 'ow-power-label' }, h('span')));
	// The poll redraws the page on every phase. The new button shows the right look from the start;
	// on its first frame it eases over from what the button it replaces showed, its pulse in step.
	const replacing = !!ui.memory.power?.el?.deref()?.isConnected;
	shown = paintPower(power, look, powerWords(look, offWord()), ui.memory.power, false);
	onFirstFrame(power, ui.signal, () => {
		worn.delete(power); // its pulse starts now: time it afresh
		shown = paintPower(power, look, powerWords(look, offWord()), ui.memory.power, replacing && !reducedMotion());
		remember();
	});
	// Off, the word follows the kernel as it finishes downloading.
	kernelUpdates.addEventListener('change', () => look === 'off' && !preparing && show('off'), { signal: ui.signal });
	// What runs once the page is on screen.
	const onShown = [];
	ui.afterRender = () => {
		for (const fn of onShown) fn();
	};
	// Under the status line: why the proxy is not running as asked, or what the last start said.
	// Either keeps the proxy from working, so it is announced at once, but only as news. The page
	// redraws at every phase, and after a fault the router's retries clear the problem while they
	// start and set it again when they fail: the same problem back in a new line shows without
	// being read out again, until the proxy runs or stops.
	const problem = ui.status.error ? diagnosticMessage(ui.status.error) : '',
		settled = ui.status.phase === 'running' || ui.status.phase === 'direct';
	const powerLine = ui.feedback(power, null, { assertive: true });
	if (problem) {
		if (ui.memory.problem === problem) powerLine.setAttribute('role', 'status');
		say(powerLine, [problem, ' ', ui.button(t('查看日志'), () => ui.go('logs'), 'ow-link')], { control: power });
	}
	onShown.push(() => {
		if (problem) ui.memory.problem = problem;
		else if (settled) delete ui.memory.problem;
	});
	let history = (ui.memory.traffic ||= []);
	if (!running || ui.memory.run !== ui.status.running_since) {
		history.length = 0;
		ui.memory.run = ui.status.running_since;
	}
	const graph = h('div', { class: 'ow-chart' }),
		total = h('strong', {}, '—'),
		rate = h('span', {}, '—');
	function traffic(sample) {
		if (sample?.available) {
			history.push({ ...sample, t: Date.now() });
			while (history.length && Date.now() - history[0].t > 60000) history.shift();
		}
		const last = history.at(-1);
		total.textContent = last ? t('↓ {0}　↑ {1}', bytes(last.down_bytes), bytes(last.up_bytes)) : '—';
		rate.replaceChildren(
			h('span', {}, '↓ ' + (last ? bytes(last.down_bytes_per_second) + '/s' : '—')),
			h('span', {}, '↑ ' + (last ? bytes(last.up_bytes_per_second) + '/s' : '—')),
		);
		graph.replaceChildren(chart(history));
	}
	// Once the core runs and the traffic is taken over, the router waits a few seconds for the
	// core to stay healthy before it counts the start; those seconds count down here every second,
	// between status reads. The router rounds up, so the earliest end it has given is the closest;
	// one more than a second later belongs to a new attempt.
	let confirmEnd = 0;
	function confirming(st) {
		if (!(st.confirmation_seconds > 0)) return;
		const end = Date.now() + st.confirmation_seconds * 1000;
		confirmEnd = confirmEnd && end <= confirmEnd + 1000 ? Math.min(confirmEnd, end) : end;
	}
	// Short enough for the line beside the button on a phone.
	function timer() {
		if (ui.status.phase === 'starting' && confirmEnd) {
			const left = Math.ceil((confirmEnd - Date.now()) / 1000);
			sub.textContent = t('已接管流量，确认中{0}', left > 0 ? t(' {0} 秒', left) : '');
			return;
		}
		sub.textContent =
			running && ui.status.running_since
				? t('已运行 {0}', uptime(Date.now() - ui.status.running_since))
				: phaseDetail[ui.status.phase] || '';
	}
	confirming(ui.status);
	timer();
	ui.poll(timer, 1000);
	traffic(running ? await ui.rpc('traffic').catch(() => null) : null);
	const statusSide = h(
		'div',
		{ class: 'ow-connection' },
		header,
		sub,
		powerLine,
		power,
		h('strong', { class: 'ow-plan-label' }, cat.primary?.name || t('尚未选择订阅')),
		h('small', { class: 'ow-muted' }, devices.unavailable ? t('等待局域网接口') : t('{0} 台局域网设备', devices.items.length)),
		running && ui.status.side_router && h('small', { class: 'ow-muted' }, t('旁路由模式：设备的网关和 DNS 都需要指向本机')),
		h(
			'div',
			{ class: 'ow-graph-block' },
			h('div', { class: 'ow-total' }, t('本次流量'), total),
			graph,
			h('div', { class: 'ow-rate' }, t('近 1 分钟'), rate),
		),
	);
	const routeBox = h('div', { class: 'ow-routes' });
	function routeRows(rows) {
		routeBox.replaceChildren(
			...rows.map((r) => {
				const cell = h(
					r.group ? 'button' : 'div',
					r.group
						? { type: 'button', class: 'ow-route', onClick: () => ui.go('proxy', r.group) }
						: { class: 'ow-route' },
					h(
						'span',
						{ class: 'ow-route-icon', 'aria-hidden': 'true' },
						r.outbound === '@direct' ? '↗' : r.outbound === '@reject' ? '⊘' : '⇢',
					),
					h(
						'span',
						{},
						h('strong', {}, r.group ? ui.name(r.group) : ui.name(r.outbound)),
						h(
							'small',
							{},
							r.group
								? ui.name(r.outbound)
								: r.evidence === 'traffic_observed'
									? t('已建立')
									: resultName[r.evidence] || t('已分流'),
						),
					),
				);
				cell.title = clock(r.time);
				return cell;
			}),
		);
		if (!rows.length) routeBox.append(empty(t('暂无实际代理路线')));
	}
	routeRows(routes.items || []);
	const modes = h(
		'div',
		{ class: 'ow-modes' },
		field(
			t('代理模式'),
			focusKeys(segment(ui, t('代理模式'), modeNames, intent.mode, async (mode) => {
				await ui.run('mode', { mode, revision: intent.revision });
				await ui.refresh();
			}), 'mode'),
		),
	);
	const right = h('div', { class: 'ow-route-side' }, modes, ui.feedback(modes));
	if (intent.mode === 'global') {
		const groups = await ui.rpc('groups'),
			sel = groups['@global'];
		const nodes = cat.nodes.filter((n) => !n.special && n.availability !== 'unsupported');
		const saved = sel?.saved || nodes[0]?.id;
		const grid = h('div', { class: 'ow-routes ow-global-nodes', role: 'group', 'aria-label': t('全局节点') });
		const gridLine = ui.feedback(grid);
		for (const n of nodes) {
			const cell = h(
				'button',
				{ type: 'button', class: 'ow-route', 'aria-pressed': String(n.id === saved), 'data-focus-key': 'node:' + n.id },
				h('span', { class: 'ow-route-icon', 'aria-hidden': 'true' }, '⇢'),
				h(
					'span',
					{},
					h('strong', {}, n.name),
					h(
						'small',
						{},
						n.id === sel?.applied ? t('实际出口') : n.id === saved ? t('已选择 · 启动后生效') : n.protocol || '',
					),
				),
			);
			cell.addEventListener('click', async () => {
				if (n.id === saved) return;
				hush(gridLine);
				try {
					await ui.run('select_node', { source: intent.source, group: '@global', node: n.id });
					await ui.refresh();
				} catch (e) {
					if (e.name !== 'AbortError') ui.report(cell, e);
				}
			});
			grid.append(cell);
		}
		right.append(
			h(
				'div',
				{ class: 'ow-row' },
				h('h3', {}, t('全局节点')),
				ui.button(t('管理代理'), () => ui.go('proxy'), 'ow-link'),
			),
			...(nodes.length ? [grid, gridLine] : [empty(t('当前订阅没有可用节点'))]),
		);
	} else if (intent.mode === 'direct') {
		right.append(
			h(
				'div',
				{ class: 'ow-row' },
				h('h3', {}, t('全屋直连')),
				ui.button(t('管理代理'), () => ui.go('proxy'), 'ow-link'),
			),
		);
	} else
		right.append(
			h(
				'div',
				{ class: 'ow-row' },
				h('h3', {}, t('代理路线')),
				h(
					'div',
					{ class: 'ow-actions' },
					ui.button(t('管理代理'), () => ui.go('proxy'), 'ow-link'),
					ui.button(t('连接记录 ›'), () => ui.go('connections'), 'ow-link'),
				),
			),
			routeBox,
		);
	const probes = h('div', { class: 'ow-checks' }),
		check = ui.button(
			t('检测'),
			async () => {
				const res = await ui.run('health_check', {});
				ui.memory.checks = { since: ui.status.running_since, result: res };
				drawChecks();
			},
			'ow-link',
		);
	function drawChecks() {
		const v =
			ui.memory.checks?.since === ui.status.running_since && running ? ui.memory.checks?.result : null;
		probes.replaceChildren(
			...[
				['core', t('核心')],
				['dns', 'DNS'],
				['baidu', t('百度')],
				['netease', t('网易云')],
				['google', 'Google'],
				['youtube', 'YouTube'],
				['github', 'GitHub'],
			].map(([k, n]) =>
				h(
					'span',
					{},
					h('i', {
						class:
							'ow-dot' + (v?.[k] ? (v[k].ok ? ' good' : ' bad') : k === 'core' && running ? ' good' : ''),
					}),
					n +
						(v?.[k] ? ' ' + (v[k].ok ? v[k].delay_ms + ' ms' : t('超时')) : k === 'core' && running ? t(' 运行中') : ''),
				),
			),
			check,
		);
		check.disabled = !running;
	}
	drawChecks();
	right.append(probes, ui.feedback(probes));
	const hero = h('section', { class: 'ow-card ow-hero', 'aria-label': t('连接状态') }, statusSide, right);
	function toggle(label, value, fn) {
		const b = ui.button('', () => fn(!value), 'ow-switch');
		b.dataset.focusKey = 'switch:' + label;
		b.setAttribute('role', 'switch');
		b.setAttribute('aria-label', label);
		b.setAttribute('aria-checked', String(value));
		return b;
	}
	function choose(label, options, value, fn) {
		const select = h('select', { 'aria-label': label, 'data-focus-key': 'select:' + label },
			...options.map(([v, name]) => h('option', { value: String(v) }, name)));
		select.value = String(value);
		select.addEventListener('change', async () => {
			const v = options.find(([o]) => String(o) === select.value)[0];
			try {
				await fn(v);
			} catch (e) {
				if (e.name !== 'AbortError') ui.tell(label, diagnosticMessage(e.message), 'error');
				select.value = String(value);
			}
		});
		return select;
	}
	// A setting that fails to save says so on the line under its row.
	const lined = (row, key) => [row, ui.feedback(row, key)];
	const proxyPrefs = card(
		t('代理'),
		lined(h(
			'div',
			{ class: 'ow-row' },
			t('新设备默认'),
			focusKeys(segment(
				ui,
				t('新设备默认'),
				[
					['proxy', t('代理')],
					['direct', t('直连')],
				],
				policy.default_direct ? 'direct' : 'proxy',
				(v) => savePolicy({ default_direct: v === 'direct' }),
			), 'default'),
		)),
		lined(updateRow(t('大陆 IP 直连'), toggle(t('大陆 IP 直连'), !!prefs.bypass_cn, (v) => saveP({ bypass_cn: v })),
			h('small', { class: 'ow-muted' }, t('国内 IP 不经内核，更快；全局模式下不生效')))),
		lined(updateRow(t('路由器自身流量走代理'), toggle(t('路由器自身流量走代理'), prefs.router_self !== false, (v) => saveP({ router_self: v })),
			h('small', { class: 'ow-muted' }, t('比如路由器上的软件更新、节点同步')))),
		lined(h(
			'div',
			{ class: 'ow-row' },
			t('开机启动'),
			toggle(t('开机启动'), prefs.enabled, (v) => saveP({ enabled: v })),
		)),
	);
	const dnsOnly = !!prefs.dns_only;
	const dnsSwitch = toggle(t('DNS 保护'), prefs.dns || dnsOnly, (v) => saveP({ dns: v }));
	if (dnsOnly) dnsSwitch.disabled = true;
	const dnsPrefs = card(
		'DNS',
		lined(updateRow(t('DNS 保护'), dnsSwitch,
			h('small', { class: 'ow-muted' }, dnsOnly ? t('仅 DNS 保护时始终开启') : t('由内核解析，防污染')))),
		lined(updateRow(t('仅 DNS 保护'), toggle(t('仅 DNS 保护'), dnsOnly, (v) => saveP({ dns_only: v, ...(v ? { dns: true } : {}) })),
			h('small', { class: 'ow-muted' }, t('只加密 DNS 查询，防劫持和污染；所有流量直连，不经过代理')))),
		upstreamRow(ui, t('国内域名'), t('直连解析'), 'domestic', prefs.dns_domestic, (v) => saveP({ dns_domestic: v })),
		upstreamRow(ui, t('其他域名'), t('经代理加密解析'), 'foreign', prefs.dns_foreign, (v) => saveP({ dns_foreign: v })),
	);
	const failOpen = prefs.fail_open !== false;
	const failOpenLabel = t('代理无响应时回退直连');
	const fallback = toggle(failOpenLabel, failOpen, (v) => saveP({ fail_open: v }));
	fallback.setAttribute('aria-describedby', 'ow-fail-open-help');
	// Recommended settings; the LAN networks, start at boot and the update period are kept.
	const restoreDefaults = () => {
		const d = ui.dialog(t('恢复默认设置'), h('div', { class: 'ow-stack' },
			h('p', {}, t('DNS、大陆 IP 直连、故障回退和节点自动切换将恢复为推荐的默认值，代理会重启一次。')),
			h('small', { class: 'ow-muted' }, t('订阅、规则、设备设置和管理的局域网不受影响。')),
			h('div', { class: 'ow-row' },
				ui.button(t('取消'), () => d.close()),
				// A failure reads in the dialog's status line.
				ui.button(t('恢复默认'), async () => {
					await ui.run('policy', { revision: intent.revision, policy: { ...policy, auto_failover: undefined } });
					await ui.refresh();
					await ui.run('save_network', { args: {
						...prefs, dns: true, fail_open: true, bypass_cn: false,
						dns_domestic: builtinDNS.domestic, dns_foreign: builtinDNS.foreign,
					} });
					await ui.refresh();
					d.close();
					ui.tell('repair', t('已恢复默认设置'), 'done');
				}, 'ow-danger')),
		));
	};
	const restore = ui.button(t('恢复默认'), restoreDefaults, 'ow-link');
	restore.dataset.focusKey = 'restore-defaults';
	const repair = card(
		'',
		// 恢复默认 confirms here once its dialog closes.
		lined(h('div', { class: 'ow-row' }, h('h2', {}, t('故障与恢复')), restore), 'repair'),
		lined(h('div', { class: 'ow-row' }, t('防火墙'), ui.button(t('检查运行环境'), async () => {
			const env = await ui.rpc('environment');
			if (!ui.signal.aborted) ui.dialog(t('防火墙运行环境'), environmentRows(firewallDetails(env)));
		}, 'ow-link'))),
		lined(updateRow(failOpenLabel, fallback, h('small', { id: 'ow-fail-open-help', class: 'ow-muted' }, failOpen
			? t('连续 3 次检查失败后恢复直连')
			: t('内核故障时代理流量中断')))),
		lined(updateRow(t('节点失联时自动切换'),
			toggle(t('节点失联时自动切换'), policy.auto_failover !== false, (v) => savePolicy({ auto_failover: v })),
			h('small', { class: 'ow-muted' }, t('切到同组可用节点')))),
		lined(h(
			'div',
			{ class: 'ow-row' },
			t('记录连接'),
			toggle(t('记录连接'), policy.record_connections !== false, (v) => savePolicy({ record_connections: v })),
		)),
		lined(h('div', { class: 'ow-row' }, t('内核日志级别'),
			choose(t('内核日志级别'), logLevels, prefs.log_level || 'warning', (v) => saveP({ log_level: v })))),
		ui.button(t('查看日志'), () => ui.go('logs'), 'ow-link'),
	);
	const updates = card(
		t('更新'),
		lined(h(
			'div',
			{ class: 'ow-row' },
			t('订阅自动更新'),
			toggle(t('订阅自动更新'), prefs.update_hours > 0, (on) => saveP({ update_hours: on ? ui.memory.updateHours || 6 : 0 })),
		)),
		...(prefs.update_hours > 0 ? updateSchedule(ui, prefs, saveP, choose) : []),
		updateRow(t('Hyper 插件'), ...pluginUpdate(ui)),
		lined(updateRow(t('自动检查新版本'), toggle(t('自动检查新版本'), prefs.auto_check !== false, (v) => saveP({ auto_check: v })),
			h('small', { class: 'ow-muted' }, t('每天检查一次，有新版本时在这里提示')))),
		lined(updateRow(t('自动更新插件'), toggle(t('自动更新插件'), !!prefs.auto_update, (v) => saveP({ auto_update: v })),
			h('small', { class: 'ow-muted' }, t('每天检查并自动安装新版本，安装时网络会短暂中断')))),
		updateRow(...kernelSummary(ui, kernel, kernelUpdates)),
		updateRow(t('内核存储位置'), ...kernelStorage(ui, kernel, kernelUpdates, segment)),
		// The router checks these lists itself once a week; there is nothing to press.
		updateRow(t('大陆域名与 IP 列表'), h('small', { class: 'ow-muted' }, t('每周自动更新')),
			h('small', { class: 'ow-muted' }, ui.status.china_lists ? t('更新于 {0}', day(ui.status.china_lists)) : t('插件内置版本'))),
	);
	// This browser's choice: the pages follow LuCI's language unless one is chosen here.
	const general = card(
		t('界面'),
		lined(h('div', { class: 'ow-row' }, h('span', { class: 'ow-label' }, t('界面语言'), h('small', { class: 'ow-muted' }, t('默认跟随路由器的 LuCI 语言'))),
			choose(t('界面语言'), [['auto', t('跟随路由器')], ['zh-CN', t('简体中文')], ['en-US', 'English']], languagePreference(),
				(v) => setLanguagePreference(v)))),
	);
	// Without the account's subscription the card offers the sign-in, which syncs the nodes.
	let setup = null;
	if (!ui.sources.some((s) => !s.revoked)) setup = ui.button(t('登录 Hyper 账户'), () => ui.go('account'), 'ow-primary');
	const grid = h(
		'div',
		{ class: 'ow-dashboard' },
		hero,
		// Subscriptions share the column beside the hero.
		h('div', { class: 'ow-side-stack' }, sourceCard(ui, setup)),
		devices.unavailable ? card(t('设备'),empty(t('局域网接口尚未就绪')),ui.button(t('重新检测'),ui.refresh,'ow-link')) : deviceCard(ui, devices.items),
		proxyPrefs,
		dnsPrefs,
		repair,
		updates,
		general,
	);
	// The service's problem sits under the power button's status line, not above the page.
	const content = h('div', { class: 'ow-stack' }, grid);
	ui.poll(async () => {
		const st = await ui.rpc('status');
		if (ui.signal.aborted) return;
		// The countdown keeps in step while an open dialog holds a redraw back.
		if (st.phase === ui.status.phase) confirming(st);
		if (st.phase !== ui.status.phase || st.saved.revision !== intent.revision) {
			if (content.closest('.ow')?.querySelector('dialog[open]')) return;
			await ui.refresh();
			return;
		}
		timer();
		if (running) {
			const [t, r] = await Promise.all([ui.rpc('traffic'), ui.rpc('routes', { limit: 12 })]);
			if (ui.signal.aborted) return;
			traffic(t);
			routeRows(r.items || []);
		}
	}, 2500);
	return content;
}

const logLevels = [['silent', t('静默')], ['error', t('错误')], ['warning', t('警告')], ['info', t('信息')], ['debug', t('调试')]];

// The account is read every few hours, or once a day at a chosen hour (router time).
function updateSchedule(ui, prefs, saveP, choose) {
	const daily = prefs.update_at >= 0;
	const kind = h('div', { class: 'ow-row' }, t('更新方式'), choose(t('更新方式'), [['every', t('按间隔')], ['daily', t('每天定时')]],
		daily ? 'daily' : 'every', (v) => saveP({ update_at: v === 'daily' ? ui.memory.updateAt ?? 4 : -1 })));
	if (!daily) return [kind, ...updateInterval(ui, prefs.update_hours, saveP)];
	ui.memory.updateAt = prefs.update_at;
	const hours = Array.from({ length: 24 }, (_, n) => [n, String(n).padStart(2, '0') + ':00']);
	return [kind, h('div', { class: 'ow-row' }, h('span', { class: 'ow-label' }, t('更新时间'), h('small', { class: 'ow-muted' }, t('路由器本地时间'))),
		choose(t('更新时间'), hours, prefs.update_at, (v) => {
			ui.memory.updateAt = v;
			return saveP({ update_at: v });
		}))];
}

// 检查更新, then 更新到 vX only when a newer verified package exists.
// Title with its note underneath on the left, the control on the right, centred on both.
function updateRow(title, action, note) {
	return h('div', { class: 'ow-row' }, h('span', { class: 'ow-label' }, title, note), action);
}
function pluginUpdate(ui) {
	const action = ui.button(t('检查更新'), () => {}, 'ow-link'),
		note = h('small', { class: 'ow-muted', role: 'status' });
	let candidate = '', latest = false;
	const installed = ui.status.version && ui.status.version !== 'dev' ? ui.status.version : '';
	note.textContent = installed ? t('当前 v{0}', installed) : '';
	// What the daily check found.
	const known = ui.status.plugin;
	if (known?.available && known.candidate) {
		candidate = known.candidate;
		note.textContent = t('发现新版本 {0}', known.candidate.replace(/-r?\d+$/, ''));
	}
	show();
	function show(text) {
		action.textContent = text || (candidate ? t('更新到 {0}', candidate.replace(/-r?\d+$/, '')) : latest ? t('已是最新') : t('检查更新'));
		action.disabled = !!text || (latest && !candidate);
		action.classList.toggle('ow-primary', !text && !!candidate);
		action.classList.toggle('ow-link', !!text || !candidate);
	}
	action.addEventListener('click', async () => {
		note.textContent = '';
		delete note.dataset.tone;
		try {
			if (candidate) {
				show(t('正在更新…'));
				await ui.run('plugin_update', {});
				note.textContent = t('已安装 {0}，刷新页面后生效', candidate.replace(/-r?\d+$/, ''));
				candidate = '';
				latest = true;
				return;
			}
			show(t('正在检查…'));
			const info = await ui.run('plugin_check', {});
			candidate = info.available ? info.candidate : '';
			latest = !candidate;
			note.textContent = t('当前 v{0}', info.installed.replace(/^v/, ''));
		} catch (error) {
			if (error.name !== 'AbortError') {
				note.textContent = error.message;
				note.dataset.tone = 'error';
			}
		} finally {
			show();
		}
	});
	return [action, note];
}

// Hours between automatic subscription refreshes, typed by the user (1–720). A value out of
// range or a failed save is reported on the line under the field.
function updateInterval(ui, hours, saveP) {
	ui.memory.updateHours = hours;
	const line = ui.feedback();
	const input = h('input', {
		type: 'number', min: 1, max: 720, step: 1, inputmode: 'numeric', value: hours,
		'aria-label': t('更新间隔（小时）'), class: 'ow-hours', 'aria-describedby': line.id, 'data-focus-key': 'update-hours',
	});
	input.addEventListener('change', async () => {
		hush(line);
		const value = Number(input.value);
		if (!Number.isInteger(value) || value < 1 || value > 720) {
			say(line, t('请输入 1 到 720 之间的整数小时'));
			input.value = hours;
			return;
		}
		if (value === hours) return;
		try {
			ui.memory.updateHours = value;
			await saveP({ update_hours: value });
		} catch (e) {
			if (e.name !== 'AbortError') say(line, diagnosticMessage(e.message));
			input.value = hours;
		}
	});
	return [h('div', { class: 'ow-row' }, t('更新间隔'), h('span', { class: 'ow-actions' }, t('每'), input, t('小时'))), line];
}

// Upstreams show a short summary; a dialog edits the list and restores the built-in servers.
const presetDNS = {
	domestic: [
		[t('阿里 DoH'), 'https://223.5.5.5/dns-query'], [t('阿里 DoH 备用'), 'https://223.6.6.6/dns-query'],
		[t('腾讯 DoH'), 'https://120.53.53.53/dns-query'], [t('腾讯 DoH 备用'), 'https://1.12.12.12/dns-query'],
		[t('阿里 DoT'), 'tls://223.5.5.5'], [t('腾讯 DoT'), 'tls://1.12.12.12'],
	],
	foreign: [
		['Cloudflare DoH', 'https://1.1.1.1/dns-query'], [t('Cloudflare DoH 备用'), 'https://1.0.0.1/dns-query'],
		['Google DoH', 'https://8.8.8.8/dns-query'], [t('Google DoH 备用'), 'https://8.8.4.4/dns-query'],
		['Quad9 DoH', 'https://9.9.9.9/dns-query'], ['Cloudflare DoT', 'tls://1.1.1.1'], ['Google DoT', 'tls://8.8.8.8'],
	],
};
const builtinDNS = {
	domestic: ['https://223.5.5.5/dns-query', 'https://120.53.53.53/dns-query'],
	foreign: ['https://1.1.1.1/dns-query', 'https://8.8.8.8/dns-query'],
};
function upstreamRow(ui, title, note, kind, list, save) {
	const builtin = builtinDNS[kind];
	const current = list?.length ? list : builtin;
	const host = (v) => v.replace(/^[a-z]+:\/\//, '').replace(/\/.*$/, '');
	const same = current.join(' ') === builtin.join(' ');
	const summary = (same ? t('内置 · ') : '') + current.map(host).join(t('、'));
	const open = () => {
		let items = [...current];
		const rows = h('div', { class: 'ow-stack' });
		const presets = h('div', { class: 'ow-dns-presets' });
		// The dialog reports beside its own controls: the list limit and a failed save.
		const body = h('div', { class: 'ow-stack' });
		const line = ui.feedback(body);
		const add = (v) => {
			if (!v || items.includes(v)) return false;
			if (items.length >= 4) {
				say(line, t('最多 4 个服务器'), { control: input });
				return false;
			}
			items.push(v);
			render();
			return true;
		};
		const input = h('input', { type: 'text', placeholder: t('https://… 、tls://… 或 IP'), 'aria-label': t('添加 DNS 服务器'), spellcheck: 'false' });
		const render = () => {
			presets.replaceChildren(...presetDNS[kind].map(([name, v]) => {
				const on = items.includes(v);
				const b = ui.button(name, () => {
					if (on) items.splice(items.indexOf(v), 1);
					else if (!add(v)) return;
					render();
				}, on ? 'ow-chip ow-chip-on' : 'ow-chip');
				b.setAttribute('aria-pressed', String(on));
				b.title = v;
				return b;
			}));
			rows.replaceChildren(...items.map((v, i) => h('div', { class: 'ow-row' },
				h('code', { class: 'ow-upstream' }, v),
				ui.button(t('移除'), () => { items.splice(i, 1); render(); }, 'ow-link'))));
		};
		body.append(
			h('small', { class: 'ow-muted' }, t('常用服务器')),
			presets,
			h('small', { class: 'ow-muted' }, t('已选（按顺序使用）')),
			rows,
			h('div', { class: 'ow-row' }, input, ui.button(t('添加'), () => {
				if (add(input.value.trim())) input.value = '';
			}, 'ow-link')),
			h('div', { class: 'ow-row' },
				ui.button(t('恢复内置'), () => { items = [...builtin]; render(); }, 'ow-link'),
				ui.button(t('保存'), async (ev) => {
					if (!items.length) return say(line, t('至少保留 1 个服务器'), { control: ev.currentTarget });
					await save(items);
					d.close();
				}, 'ow-primary')),
			line,
		);
		const d = ui.dialog(title + ' DNS', body);
		render();
	};
	const manage = ui.button(t('管理'), open, 'ow-link');
	manage.dataset.focusKey = 'dns:' + kind;
	return updateRow(title, manage, h('small', { class: 'ow-muted' }, note + ' · ' + summary));
}
