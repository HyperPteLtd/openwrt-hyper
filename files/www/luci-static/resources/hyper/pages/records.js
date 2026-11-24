import { t } from '../i18n.js';
import { card, h, select, segment, empty, clock, download, resultName, eventMessage, datetime, attach, hush, setLabel, setRefs } from '../dom.js';

const levels = { info: t('信息'), warn: t('警告'), error: t('错误') };
const sources = {
	service: t('服务'),
	core: t('代理内核'),
	task: t('后台任务'),
	subscription: t('节点同步'),
	dns: 'DNS',
	firewall: t('防火墙'),
	kernel: t('内核'),
};
const outcome = (v) => (v === 'traffic_observed' ? t('已建立') : resultName[v] || v || '—');
// The router's QUIC rules, as the core reports them (router config.go): QUIC the proxy would carry
// is refused so that browsers fall back to TCP. Mainland names the proxy must carry count as foreign.
const routerRules = {
	'AND,((NETWORK,UDP),(DST-PORT,443),(RULE-SET,hyper-cn-proxied))': t('拦截 QUIC · 需代理的大陆域名'),
	'AND,((NETWORK,UDP),(DST-PORT,443),(NOT,((RULE-SET,hyper-dns-cn))),(NOT,((RULE-SET,hyper-ip-cn,no-resolve))))':
		t('拦截境外 QUIC'),
};
export async function records(ui) {
	const logs = ui.page === 'logs',
		title = logs ? t('日志') : t('连接记录');
	const state = (ui.memory[ui.page] ||= {
		follow: true,
		category: '',
		source: '',
		query: ui.detail || '',
		limit: 40,
		device: '',
		rule: '',
	});
	if (ui.detail && state.detail !== ui.detail) {
		const rule = !logs && ui.detail.startsWith('rule:') ? ui.detail.slice(5) : '';
		state.query = rule ? '' : ui.detail;
		state.rule = rule;
		state.detail = ui.detail;
	}
	const ruleSetOf = (rule) => /^RuleSet,(.+)$/.exec(rule || '')?.[1] || '';
	const ruleLabel = (rule) => {
		if (!rule) return '—';
		if (Object.hasOwn(routerRules, rule)) return routerRules[rule];
		const set = ruleSetOf(rule);
		if (set) return t('规则集 {0}', setLabel(set));
		// A logical rule names its rule sets inside its conditions.
		const [type, ...payload] = rule.split(',');
		return type === 'Match' ? t('兜底') : type + (payload.length ? ' ' + setRefs(payload.join(',')) : '');
	};
	const deviceNames = new Map();
	if (!logs) {
		const [, devices] = await Promise.all([ui.catalog(), ui.rpc('devices').catch(() => ({ items: [] }))]);
		for (const device of devices.items || [])
			for (const address of device.addresses || [])
				if (device.name && device.name !== address) deviceNames.set(address, device.name);
	}
	const deviceLabel = (address) =>
		deviceNames.has(address) ? deviceNames.get(address) + ' · ' + address : address;
	const search = h('input', {
		type: 'search',
		'aria-label': t('搜索{0}', title),
		placeholder: logs ? t('筛选消息、来源或任务 ID') : t('搜索目标、设备、策略组或节点'),
		value: state.query,
	});
	const categories = logs
		? [
				['', t('全部')],
				['warn', t('警告及以上')],
				['error', t('仅错误')],
			]
		: [
				['', t('全部')],
				['proxy', t('代理')],
				['direct', t('直连')],
				['reject', t('拦截')],
				['failed', t('失败')],
			];
	const category = segment(ui, logs ? t('日志级别') : t('连接类型'), categories, state.category, (value) => {
		state.category = value;
		for (const [i, option] of categories.entries())
			category.children[i].setAttribute('aria-pressed', String(option[0] === value));
		draw();
	});
	const source = select(t('日志来源'), [['', t('全部来源')], ...Object.entries(sources)], state.source, (v) => {
		state.source = v;
		draw();
	});
	const device = h('select', { 'aria-label': t('设备') });
	device.addEventListener('change', () => {
		state.device = device.value;
		draw();
	});
	const ruleFilter = h('select', { 'aria-label': t('命中规则集') });
	ruleFilter.addEventListener('change', () => {
		state.rule = ruleFilter.value;
		draw();
	});
	let requestId = 0;
	const follow = ui.button(
		t('实时跟随'),
		() => {
			state.follow = !state.follow;
			requestId++;
			follow.setAttribute('aria-pressed', String(state.follow));
			if (state.follow) return update();
		},
		'ow-records-follow',
	);
	follow.setAttribute('aria-pressed', String(state.follow));
	const display = h('div', {}),
		footer = h('div', { class: 'ow-records-foot' });
	let entries = [],
		shown = [];
	const columns = [t('时间'), t('设备'), t('目标'), t('策略组'), t('出口'), t('命中规则'), t('结果')];
	const rowValues = (c) => [
		clock(c.created_at),
		deviceLabel(c.device) || '—',
		c.destination || '—',
		c.chain
			?.slice()
			.reverse()
			.filter((x) => x.startsWith('group:') || x.startsWith('local:') || ['@global', '@default'].includes(x))
			.map(ui.name)
			.join(' → ') || '—',
		ui.name(c.outbound),
		ruleLabel(c.rule),
		outcome(c.result),
	];
	function draw() {
		const q = state.query.trim().toLocaleLowerCase();
		shown = entries.filter((v) => {
			if (logs)
				return (
					(!state.category ||
						(state.category === 'warn' ? ['warn', 'error'].includes(v.level) : v.level === state.category)) &&
					(!state.source || v.source === state.source) &&
					(!q || (v.message + ' ' + eventMessage(v) + ' ' + v.source + ' ' + (sources[v.source] || '')).toLocaleLowerCase().includes(q))
				);
			const type =
				v.result === 'failed'
					? 'failed'
					: v.outbound === '@direct'
						? 'direct'
						: v.outbound === '@reject'
							? 'reject'
							: 'proxy';
			return (
				(!state.category || state.category === type) &&
				(!state.device || state.device === v.device) &&
				(!state.rule || ruleSetOf(v.rule) === state.rule) &&
				(!q || rowValues(v).join(' ').toLocaleLowerCase().includes(q))
			);
		});
		if (!logs) {
			const values = [...new Set(entries.map((c) => c.device).filter(Boolean))];
			if (state.device && !values.includes(state.device)) values.push(state.device);
			device.replaceChildren(
				h('option', { value: '' }, t('全部设备')),
				...values.map((x) => h('option', { value: x }, deviceLabel(x))),
			);
			device.value = state.device;
			const sets = [...new Set(entries.map((c) => ruleSetOf(c.rule)).filter(Boolean))].sort();
			if (state.rule && !sets.includes(state.rule)) sets.unshift(state.rule);
			ruleFilter.replaceChildren(
				h('option', { value: '' }, t('全部规则集')),
				...sets.map((x) => h('option', { value: x }, setLabel(x))),
			);
			ruleFilter.value = state.rule;
		}
		if (!shown.length)
			display.replaceChildren(empty(entries.length || state.serverQuery ? t('没有匹配的记录') : t('暂无{0}', title)));
		else if (logs)
			display.replaceChildren(
				...shown.slice(0, state.limit).map((v) => {
					const row = h('details', { class: 'ow-log', 'data-level': v.level });
					const message = eventMessage(v);
					row.append(
						h(
							'summary',
							{},
							h('time', {}, clock(v.time)),
							h('span', { class: 'ow-level ' + v.level }, levels[v.level] || v.level),
							h('span', {}, message + (v.count > 1 ? ' ×' + v.count : '')),
						),
						h(
							'div',
							{ class: 'ow-muted' },
							(sources[v.source] || v.source) + ' · ' + datetime(v.time) + ' · ' + v.message,
						),
						// A detail the sentence above quotes as it is, such as the core's words, shows once.
						...(v.detail && !message.includes(v.detail) ? [h('code', { class: 'ow-log-detail' }, v.detail)] : []),
					);
					return row;
				}),
			);
		else
			display.replaceChildren(
				h(
					'table',
					{ class: 'ow-table ow-connections' },
					h(
						'thead',
						{},
						h(
							'tr',
							{},
							columns.map((x) => h('th', { scope: 'col' }, x)),
						),
					),
					h(
						'tbody',
						{},
						shown.slice(0, state.limit).map((c) =>
							h(
								'tr',
								{},
								rowValues(c).map((v, j) =>
									h(
										'td',
										{ 'data-label': columns[j], 'data-result': j === 6 ? c.result : null },
										j === 3 && v !== '—' ? h('strong', {}, v) : v,
										j === 2 && c.network ? h('small', {}, c.network.toUpperCase()) : null,
									),
								),
							),
						),
					),
				),
			);
		footer.replaceChildren(
			shown.length > state.limit
				? ui.button(
						t('加载更早记录'),
						() => {
							state.limit += 40;
							draw();
						},
						'ow-link',
					)
				: h('small', { class: 'ow-muted' }, t('{0} 条记录', shown.length)),
		);
	}
	async function readRows(query, current = () => true) {
		const rows = [];
		for (let offset = 0; offset < 512 && current(); offset += 100) {
			const data = await ui.rpc(ui.page, {
				offset,
				limit: Math.min(100, 512 - offset),
				...(query ? { query } : {}),
			});
			if (ui.signal.aborted || !current()) return [];
			rows.push(...(data.items || []));
			if ((data.items || []).length < 100) break;
		}
		return rows;
	}
	async function update() {
		const id = ++requestId;
		const rows = await readRows(state.serverQuery, () => id === requestId);
		if (ui.signal.aborted || id !== requestId) return;
		if (JSON.stringify(rows) !== JSON.stringify(entries)) {
			entries = rows;
			draw();
		}
	}
	search.addEventListener('input', () => {
		state.query = search.value;
		state.limit = 40;
		draw();
	});
	const remoteSearch =
		logs &&
		h('input', {
			type: 'search',
			'aria-label': t('搜索路由器日志'),
			placeholder: t('搜索路由器日志'),
			value: state.serverQuery || '',
		});
	// What the toolbar's search and actions report sits under the toolbar.
	const line = ui.feedback();
	if (remoteSearch) {
		let timer;
		remoteSearch.setAttribute('aria-describedby', line.id);
		remoteSearch.addEventListener('input', () => {
			requestId++;
			state.serverQuery = remoteSearch.value.trim();
			state.limit = 40;
			clearTimeout(timer);
			hush(line);
			timer = setTimeout(() => update().catch((e) => {
				if (e.name !== 'AbortError') ui.report(remoteSearch, e);
			}), 250);
		});
		ui.signal.addEventListener('abort', () => clearTimeout(timer), { once: true });
	}
	const text = () => JSON.stringify(shown, null, 2);
	const toolbar = h('div', { class: 'ow-toolbar' }, category, logs ? source : [device, ruleFilter], search, remoteSearch);
	attach(toolbar, line);
	const header = h('div', { class: 'ow-records-header' }, h('h2', {}, title), follow);
	// 实时跟随 and 导出诊断 report on the line under the header.
	const headerLine = ui.feedback(header);
	if (logs)
		header.append(
			ui.button(t('导出诊断'), async () => {
				const [status, kernel, env, events] = await Promise.all([
					ui.rpc('status'),
					ui.rpc('kernel_read', { action: 'status' }),
					ui.rpc('environment'),
					state.serverQuery ? readRows('') : entries,
				]);
				const report = {
					schema_version: 1,
					exported_at: new Date().toISOString(),
					phase: status.phase,
					mode: status.saved.mode,
					fail_open: status.fail_open,
					core: {
						target: kernel.target,
						active: kernel.active,
						selected: kernel.selected,
						trust_ready: kernel.trust_ready,
					},
					environment: env,
					// Codes, counts and settings only: no names, links, servers or notice details.
					sources: ui.sources.map((s) => {
						const template = s.template;
						return {
							kind: s.kind,
							ready: s.ready,
							format: s.format || null,
							node_count: s.node_count,
							rule_count: s.rule_count,
							// The 分流规则 setting (unset: the subscription's own rules, or the built-in
							// template when they are missing or too lossy) and what the current profile
							// routes by: its own rules, the template, or the router's local default.
							routing: s.rules || 'auto',
							routing_in_effect: !s.ready ? null : s.local ? 'local' : template ? 'template' : 'subscription',
							local: !!s.local,
							local_fallback: !!s.local_fallback,
							template: t
								? { version: t.version, reason: t.reason, cached: !!t.cached, dropped: t.dropped || 0, total: t.total || 0 }
								: null,
							last_failure: s.last_failure || null,
							notices: [...new Set((s.notices || []).map((n) => n.code))],
						};
					}),
					events: events.map(({ time, source, level, message, detail }) => ({ time, source, level, message, detail })),
				};
				download('hyper-diagnostics.json', JSON.stringify(report, null, 2));
			}),
		);
	const actions = h(
		'div',
		{ class: 'ow-actions ow-records-actions' },
		ui.button(
			t('复制'),
			async () => {
				if (navigator.clipboard?.writeText) {
					await navigator.clipboard.writeText(text());
					return;
				}
				const area = h('textarea', { 'aria-label': t('复制筛选结果'), readonly: true }, text());
				ui.dialog(t('复制筛选结果'), area);
				area.select();
			},
			'ow-link',
		),
		ui.button(t('导出筛选结果'), () => download('hyper-' + ui.page + '.json', text()), 'ow-link'),
		ui.button(
			t('清空'),
			() => {
				const d = ui.dialog(
					t('清空{0}', title),
					h(
						'div',
						{ class: 'ow-stack' },
						h('p', {}, t('清空此页的所有记录？')),
						ui.button(
							t('清空{0}', title),
							async () => {
								await ui.rpc('clear', { page: ui.page });
								d.close();
								entries = [];
								draw();
								await update();
							},
							'ow-danger',
						),
					),
				);
			},
			'ow-link',
		),
	);
	toolbar.classList.add('ow-records-toolbar');
	toolbar.append(actions);
	draw();
	await update();
	ui.poll(async () => {
		if (state.follow && !display.querySelector('details[open]')) await update();
	});
	return h('div', { class: 'ow-records-page' }, header, headerLine, card(null, toolbar, line, display, footer));
}
