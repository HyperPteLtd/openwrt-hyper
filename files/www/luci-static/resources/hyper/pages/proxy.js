import { h, card, field, select, segment, empty, pages, attach } from '../dom.js';

const kindName = { selector: '手选', urltest: '自动', loadbalance: '负载均衡', fallback: '故障转移' };
const protocolNames = {
	vless: 'VLESS',
	vmess: 'VMess',
	trojan: 'Trojan',
	snell: 'Snell',
	anytls: 'AnyTLS',
	hysteria2: 'Hysteria2',
	tuic: 'TUIC',
	shadowsocks: 'Shadowsocks',
	shadowsocksr: 'ShadowsocksR',
	socks: 'SOCKS5',
	socks5: 'SOCKS5',
	http: 'HTTP',
	https: 'HTTPS',
	naive: 'Naive',
	mieru: 'Mieru',
	nowhere: 'Nowhere',
};

export async function proxy(ui) {
	const { nodes, groups: allGroups, primary } = await ui.catalog();
	const groups = allGroups.filter(g => !g.hidden || g.id === ui.detail);
	if (!primary)
		return h(
			'div',
			{ class: 'ow-proxy-page' },
			h('h2', {}, '代理'),
			card(
				null,
				empty('选择订阅后可查看节点'),
				ui.button('管理订阅', () => ui.go('subscriptions'), 'ow-primary'),
			),
		);
	const { intent } = ui.status,
		global = intent.mode === 'global';
	let selections = {},
		live = new Map(),
		timings = {};
	const state = (ui.memory.proxy ||= { open: new Set(), query: '', sort: 'default' });
	state.method ||= 'url';
	if (ui.detail) state.open.add(ui.detail);
	const search = h('input', {
		type: 'search',
		placeholder: '搜索节点或策略组',
		'aria-label': '搜索节点或策略组',
		value: state.query,
	});
	const body = h('div', { class: global ? 'ow-global-body' : 'ow-group-columns' });
	const list = nodes.filter((n) => !n.special);
	const revisions = new Map(),
		displayLimits = new Map();
	const data = new Map([...nodes, ...allGroups].map((x) => [x.id, x]));
	const label = (id) => data.get(id)?.name || ui.name(id);
	const matches = (s) => String(s).toLocaleLowerCase().includes(state.query.trim().toLocaleLowerCase());
	const preferred = (id) => (ui.status.phase === 'running' ? live.get(id) || selections[id]?.applied : '');
	const compact = window.matchMedia('(max-width: 640px)');
	const initialLimit = () => (compact.matches && !global ? 6 : 96);
	let probeJob = null;
	let snapshotRevision = 0;
	// Each group panel reports its choices and tests on its own line. Panels are redrawn as
	// results arrive, so the line outlives them and each new panel reports to it.
	const lines = new Map();
	const reported = (panel, id) => {
		if (!lines.has(id)) lines.set(id, ui.feedback());
		panel.append(attach(panel, lines.get(id)));
		return panel;
	};
	const bulk = ui.button(
		'全部测速',
		() => {
			const visible = global
				? list.filter((n) => matches(n.name + ' ' + n.protocol)).map((n) => n.id)
				: groups.flatMap((g) =>
						matches(g.name)
							? g.members
							: g.members.filter((id) => matches(label(id) + ' ' + (data.get(id)?.protocol || ''))),
					);
			return measure(visible);
		},
		'ow-primary',
	);
	const canMeasure = () => state.method === 'tcp' || ui.status.phase === 'running';
	bulk.disabled = !canMeasure();
	const extra = h('span', { class: 'ow-proxy-extra' });
	const cancel = ui.button('取消测速', () => ui.run('cancel_tests', {}), 'ow-link');
	async function measure(ids) {
		const candidates = new Set();
		for (const candidate of ids) {
			// A group member has a measurable endpoint only when its live group
			// chain leads to a particular node, including cross-source members.
			const id = route(candidate).at(-1);
			if (
				id?.startsWith('node:') &&
				!data.get(id)?.special && data.get(id)?.availability !== 'unsupported' &&
				(state.method !== 'tcp' || data.get(id)?.tcp_probe !== false)
			)
				candidates.add(id);
		}
		if (!candidates.size) return;
		const job = { method: state.method, candidates };
		probeJob = job;
		toolbar.append(cancel);
		draw();
		try {
			await ui.run('test_nodes', {
				source: intent.source,
				method: job.method,
				args: { nodes: [...candidates] },
			});
		} catch (err) {
			// Cancelling or superseding a batch finishes its task with this context error.
			if (err.message !== 'context canceled') throw err;
		} finally {
			if (probeJob === job) {
				probeJob = null;
				cancel.remove();
				if (!ui.signal.aborted) {
					draw();
					await update();
				}
			}
		}
	}
	function route(id) {
		const chain = [],
			seen = new Set();
		while (id && !seen.has(id)) {
			seen.add(id);
			chain.push(id);
			id = preferred(id);
		}
		return chain;
	}
	const measured = (id) => {
		const sample = timings[id];
		if (
			state.method === 'tcp' &&
			(!id?.startsWith('node:') || data.get(id)?.tcp_probe === false || sample?.error === 'not_applicable')
		)
			return '不支持';
		if (probeJob?.method === state.method && probeJob.candidates.has(id)) return '测速中';
		return sample?.latency
			? sample.latency.delay_ms + ' ms'
			: sample?.error
				? '未响应'
				: '';
	};
	function tile(id, g, chosen, actual) {
		const node = data.get(id),
			picked = chosen === id,
			active = actual === id;
		const name = h('strong', { title: label(id) }, label(id));
		const memberRoute = route(preferred(id));
		const used = memberRoute.at(-1) || id;
		const unavailable = node?.availability === 'unsupported' ? '当前内核不支持' :
			node?.availability === 'unverified' ? '待选择内核后确认' : '';
		const detail = h(
			'small',
			{ class: 'ow-muted' },
			unavailable || (memberRoute.length
				? '→ ' + memberRoute.map(label).join(' → ')
				: protocolNames[node?.protocol] ||
						node?.protocol ||
						kindName[node?.kind] ||
						(id === '@direct' ? '直连' : id === '@reject' ? '拦截' : '策略组')),
		);
		const status = active
			? global
				? '当前出口'
				: '使用中'
			: picked
				? selections[g.id]?.saved
					? '已保存'
					: '默认'
				: '';
		const content = [
			h(
				'span',
				{ class: 'ow-node-copy' },
				h('span', { class: 'ow-node-name' }, name, status && h('span', { class: 'ow-badge' }, status)),
				detail,
			),
			h(
				'span',
				{ class: 'ow-node-tail', 'data-slow': timings[used]?.latency?.delay_ms >= 150 },
				measured(used),
			),
		];
		if (g.kind !== 'selector') return h('div', { class: 'ow-node' + (active ? ' is-active' : '') }, content);
		const b = ui.button(
			'',
			async () => {
				const seq = (revisions.get(g.id) || 0) + 1;
				revisions.set(g.id, seq);
				snapshotRevision++;
				await ui.run('select_node', { source: intent.source, group: g.id, node: id });
				const current = await ui.rpc('groups');
				if (!ui.signal.aborted && revisions.get(g.id) === seq) {
					selections[g.id] = current[g.id];
					if (current[g.id]?.state === 'applied') live.set(g.id, current[g.id].applied);
					snapshotRevision++;
					draw();
				}
			},
			'ow-node' + (picked ? ' is-active' : ''),
		);
		b.disabled = node?.availability === 'unsupported';
		b.setAttribute('aria-pressed', String(picked));
		b.dataset.member = id;
		b.dataset.group = g.id;
		b.append(...content);
		return b;
	}
	function memberGrid(g, found) {
		const actual = preferred(g.id),
			chosen = selections[g.id]?.saved || actual || (g.kind === 'selector' ? g.default || g.members[0] : '');
		const ids = g.members.filter((id) => found || matches(label(id) + ' ' + (data.get(id)?.protocol || '')));
		if (state.sort === 'name') ids.sort((a, b) => label(a).localeCompare(label(b)));
		if (state.sort === 'delay')
			ids.sort(
				(a, b) => (timings[a]?.latency?.delay_ms ?? Infinity) - (timings[b]?.latency?.delay_ms ?? Infinity),
			);
		const limit = displayLimits.get(g.id) || initialLimit();
		return h(
			'div',
			{ class: 'ow-node-grid' },
			ids.slice(0, limit).map((id) => tile(id, g, chosen, actual)),
			ids.length > limit &&
				ui.button(
					'显示更多节点（' + (ids.length - limit) + '）',
					() => {
						displayLimits.set(g.id, limit + 96);
						draw();
					},
					'ow-link ow-node-more',
				),
		);
	}
	function retry(g, saved) {
		if (!saved?.error) return null;
		return h(
			'div',
			{ class: 'ow-selection-error', role: 'alert' },
			saved.error,
			ui.button(
				'重试',
				async () => {
					await ui.run('select_node', { source: intent.source, group: g.id, node: saved.saved });
					await update();
				},
				'ow-link',
			),
		);
	}

	function draw() {
		const active = document.activeElement,
			prev = active?.dataset?.member,
			prevGroup = active?.dataset?.group,
			groupControl = active?.dataset?.control;
		const fragment = document.createDocumentFragment();
		if (global) {
			const group = { id: '@global', kind: 'selector', members: list.map((n) => n.id) };
			const saved = selections[group.id],
				actual = preferred(group.id);
			const panel = card(
				null,
				h(
					'div',
					{ class: 'ow-global-heading' },
					h('h3', {}, '全局'),
					h('small', {}, list.length + ' 个节点 · ' + (primary.name || primary.source)),
					saved?.saved &&
						saved.saved !== actual &&
						h('span', { class: 'ow-global-saved' }, '已保存 · ' + label(saved.saved)),
					actual && h('span', { class: 'ow-global-applied' }, '当前出口 · ' + label(actual)),
				),
				memberGrid(group, false),
				retry(group, saved),
			);
			if (!panel.querySelector('.ow-node')) panel.append(empty(state.query ? '没有匹配项' : '暂无可用节点'));
			fragment.append(reported(panel, group.id));
		} else {
			const panels = [];
			for (const [i, g] of groups.entries()) {
				const found = matches(g.name);
				if (!found && !g.members.some((id) => matches(label(id) + ' ' + (data.get(id)?.protocol || ''))))
					continue;
				const chosen = selections[g.id],
					chain = route(preferred(g.id));
				const routeText = chain.length
					? '→ ' + chain.map(label).join(' → ')
					: chosen?.saved
						? '已保存 · ' + label(chosen.saved)
						: '';
				const details = h('details', { class: 'ow-accordion' });
				details.open =
					state.query.trim() !== '' || state.open.has(g.id) || (!state.touched && !ui.detail && i === 0);
				const summary = h(
					'summary',
					{ 'data-group': g.id, 'data-control': 'expand' },
					h(
						'span',
						{ class: 'ow-group-title' },
						h('strong', {}, g.name),
						h('small', {}, (kindName[g.kind] || g.kind) + ' · ' + g.members.length),
					),
					h('span', { class: 'ow-truncate ow-group-route', title: routeText }, routeText),
					h('span', { class: 'ow-group-delay' }, measured(chain.at(-1))),
				);
				// Member dots appear once a measurement has something to show.
				const samples = g.members.slice(0, 24).map((id) => timings[route(id).at(-1)]);
				if (samples.some((s) => s?.latency || s?.error)) {
					const indicator = h('span', { class: 'ow-group-dots', 'aria-hidden': 'true' });
					for (const sample of samples)
						indicator.append(
							h('i', { 'data-speed': sample?.latency ? (sample.latency.delay_ms < 150 ? 'fast' : 'slow') : '' }),
						);
					summary.append(indicator);
				}
				summary.addEventListener('click', () => {
					if (state.query.trim()) return;
					if (!state.touched && !ui.detail && groups[0]) state.open.add(groups[0].id);
					if (details.open) state.open.delete(g.id);
					else state.open.add(g.id);
					state.touched = true;
				});
				details.append(summary);
				const syncMembers = () => {
					const grid = details.querySelector('.ow-node-grid');
					if (details.open && !grid) details.append(memberGrid(g, found));
					else if (!details.open && grid) grid.remove();
				};
				syncMembers();
				details.addEventListener('toggle', syncMembers);
				const test = ui.button('', () => measure(g.members), 'ow-group-test');
				test.dataset.group = g.id;
				test.dataset.control = 'measure';
				test.setAttribute('aria-label', '测速 ' + g.name);
				test.title = '测速 ' + g.name;
				test.disabled = !canMeasure();
				panels.push(reported(h('div', { class: 'ow-group-panel' }, details, test, retry(g, chosen)), g.id));
			}
			const middle = Math.ceil(panels.length / 2);
			for (const column of [panels.slice(0, middle), panels.slice(middle)])
				if (column.length) fragment.append(h('div', { class: 'ow-group-column' }, column));
		}
		if (!global && !fragment.querySelector('summary'))
			fragment.append(empty(state.query ? '没有匹配项' : '暂无可用节点'));
		body.replaceChildren(fragment);
		if (prevGroup && (prev || groupControl))
			[...body.querySelectorAll('[data-group]')]
				.find(
					(b) =>
						b.dataset.member === prev && b.dataset.control === groupControl && b.dataset.group === prevGroup,
				)
				?.focus({ preventScroll: true });
	}
	let signature = '';
	async function update() {
		const revision = ++snapshotRevision;
		const [s, t, l] = await Promise.all([
			ui.rpc('groups'),
			ui.rpc('latencies', { method: state.method }),
			ui.status.phase === 'running' ? ui.rpc('live_groups').catch(() => ({ items: [] })) : { items: [] },
		]);
		if (ui.signal.aborted || revision !== snapshotRevision) return;
		const next = JSON.stringify([state.method, s, t, l.items]);
		if (next !== signature) {
			signature = next;
			selections = s;
			timings = t;
			live = new Map((l.items || []).map((group) => [group.tag, group.selected]));
			draw();
		}
	}
	search.addEventListener('input', () => {
		state.query = search.value;
		displayLimits.clear();
		draw();
	});
	compact.addEventListener('change', draw);
	ui.signal.addEventListener('abort', () => compact.removeEventListener('change', draw), { once: true });
	await update();
	ui.poll(update, 4500);
	const sort = select(
		'节点排序',
		[
			['default', '默认顺序'],
			['delay', '按延迟'],
			['name', '按名称'],
		],
		state.sort,
		(v) => {
			state.sort = v;
			draw();
		},
	);
	const methods = [
		['url', 'URL'],
		['tcp', 'TCP'],
	];
	const methodSelector = segment(ui, '测速方式', methods, state.method, async (method) => {
		if (method === state.method) return;
		state.method = method;
		for (const [i, [name]] of methods.entries())
			methodSelector.children[i].setAttribute('aria-pressed', String(method === name));
		timings = {};
		bulk.disabled = !canMeasure();
		draw();
		await update();
	});
	const toolbar = h(
		'div',
		{ class: 'ow-toolbar ow-proxy-toolbar' },
		h('h2', {}, '代理'),
		h('small', { class: 'ow-method-label' }, '测速方式'),
		methodSelector,
		sort,
		search,
		extra,
		bulk,
	);
	if (!global) extra.append(ui.button('本地策略组', () => localGroups(ui)));
	if (nodes.some((n) => n.special && n.protocol === 'mtproxy') || intent.policy?.telegram)
		extra.append(ui.button('Telegram 入口', () => telegram(ui, nodes)));
	// 全部测速, the test method and the entry dialogs report under the toolbar.
	return h('div', { class: 'ow-proxy-page' }, toolbar, ui.feedback(toolbar), body);
}
async function localGroups(ui) {
	const policy = ui.status.intent.policy || {},
		existing = policy.groups || [];
	const all = [];
	for (const s of ui.sources.filter((x) => x.ready))
		for (const n of await pages(ui, s.source, 'nodes')) if (!n.special && n.availability !== 'unsupported') all.push({ s, n });
	const title = h('input', { required: true, maxlength: 128 }),
		members = h('div', { class: 'ow-picker' }),
		choices = [];
	for (const { s, n } of all) {
		const b = h('input', { type: 'checkbox' });
		choices.push({ b, ref: { source: s.source, node: n.id } });
		members.append(h('label', {}, b, n.name, h('small', { class: 'ow-muted' }, s.name || s.source)));
	}
	const edit = select('本地策略组', [['', '新建策略组'], ...existing.map((g) => [g.id, g.name])], '', (id) => {
		const g = existing.find((x) => x.id === id);
		title.value = g?.name || '';
		for (const { b, ref } of choices)
			b.checked = !!g?.members.some((r) => r.node === ref.node && r.source === ref.source);
	});
	const apply = async (groups) => {
		await ui.run('policy', { revision: ui.status.intent.revision, policy: { ...policy, groups } });
		d.close();
		await ui.refresh();
	};
	const form = h(
		'div',
		{ class: 'ow-stack' },
		field('策略组', edit),
		field('名称', title),
		members,
		h(
			'div',
			{ class: 'ow-actions' },
			ui.button(
				'删除',
				async () => {
					if (edit.value) await apply(existing.filter((x) => x.id !== edit.value));
				},
				'ow-danger',
			),
			ui.button(
				'保存',
				async () => {
					if (!title.reportValidity()) return;
					const selected = choices.filter((c) => c.b.checked).map((c) => c.ref);
					if (!selected.length) throw Error('请至少选择一个节点');
					const id =
						edit.value ||
						'local:' + Array.from(crypto.getRandomValues(new Uint32Array(2)), (x) => x.toString(16)).join('');
					await apply([
						...existing.filter((x) => x.id !== id),
						{ id, name: title.value.trim(), members: selected },
					]);
				},
				'ow-primary',
			),
		),
	);
	const d = ui.dialog('本地策略组', form);
}
function telegram(ui, nodes) {
	const policy = ui.status.intent.policy || {},
		old = policy.telegram;
	const choose = select(
		'Telegram 节点',
		nodes.filter((n) => n.special && n.protocol === 'mtproxy').map((n) => [n.id, n.name]),
		old?.node.node || nodes.find((n) => n.special)?.id,
	);
	const i = h('input', { value: old?.interface || '', placeholder: 'br-lan' }),
		a = h('input', { value: old?.address || '', placeholder: '192.168.1.1' }),
		p = h('input', { type: 'number', min: 1024, max: 65535, value: old?.port || 23456 }),
		c = h('input', { value: old?.allow?.join(', ') || '', placeholder: '192.168.1.0/24' });
	const save = async (value) => {
		await ui.run('policy', { revision: ui.status.intent.revision, policy: { ...policy, telegram: value } });
		d.close();
		await ui.refresh();
	};
	const d = ui.dialog(
		'Telegram 专用入口',
		h(
			'div',
			{ class: 'ow-stack' },
			field('Telegram 节点', choose),
			field('LAN 接口', i),
			field('LAN 地址', a),
			field('监听端口', p),
			field('允许的客户端网段', c),
			h(
				'div',
				{ class: 'ow-actions' },
				old && ui.button('停用', () => save(null)),
				ui.button(
					'保存',
					() =>
						save({
							node: { source: ui.status.intent.source, node: choose.value },
							interface: i.value.trim(),
							address: a.value.trim(),
							port: Number(p.value),
							allow: c.value.split(',').map((s) => s.trim()),
						}),
					'ow-primary',
				),
			),
		),
	);
}
