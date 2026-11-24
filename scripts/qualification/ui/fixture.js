// Deterministic rpcd replies for the LuCI pages without a router: a router signed in to a Hyper
// account (or not, with signedOut), its synced nodes and the account service's replies. Names are
// ASCII so that any Chinese left on an English page is the interface's own.
export function fixture({ signedOut = false } = {}) {
	const id = (n) => 'node:' + String(n).padStart(32, '0');
	const nodes = ['Tokyo 01', 'Singapore 02', 'Los Angeles 03'].map((name, i) => ({
		name, id: id(i + 1), protocol: ['anytls', 'trojan', 'hysteria2'][i], tcp_probe: true,
	}));
	const hyperGroup = 'group:00000000000000000000000000000hyp';
	const groups = [{ id: hyperGroup, name: 'Hyper', kind: 'selector', members: nodes.map((n) => n.id) }];
	const now = Date.now();
	const iso = (ms) => new Date(ms).toISOString();
	const sources = signedOut ? [] : [{
		source: 'hyper', name: 'Hyper', kind: 'hyper', format: 'hyper', ready: true, epoch: 1, generation: 1,
		node_count: nodes.length, group_count: 1, rule_count: 4, updated_at: iso(now - 3600e3),
		plan: { total: 2e11, upload: 5e9, download: 37e9, expireAt: iso(now + 30 * 86400e3).slice(0, 10) },
	}];
	const customRules = [
		{ id: 'legacy-direct', match: { domain_keyword: ['example'] }, target: '@direct', note: 'Work sites' },
		{ id: 'rule-2', match: { domain_suffix: ['media.example'] }, target: hyperGroup },
		{ id: 'rule-3', match: { ip_cidr: ['192.0.2.0/24'] }, target: '@reject', enabled: false },
	];
	const saved = {
		revision: 5, mode: 'smart', enabled: !signedOut, source: signedOut ? '' : 'hyper', choices: {},
		policy: signedOut ? {} : { rules: customRules, devices: [{ address: '02:00:00:01:00:02', target: '@direct' }] },
	};
	const kernel = { target: 'arm64', selected: 'v1.0.1', active: 'v1.0.1', bundled: 'v1.0.1', mode: 'double', trust_ready: true };
	let prefs = {
		method: 'tproxy', dns: true, update_hours: 6, update_at: -1, enabled: !signedOut, networks: ['lan'], fail_open: true,
		bypass_cn: false, router_self: true, log_level: 'warning', auto_check: true, auto_update: false, dns_only: false,
		dns_domestic: ['https://223.5.5.5/dns-query', 'https://120.53.53.53/dns-query'],
		dns_foreign: ['https://1.1.1.1/dns-query', 'https://8.8.8.8/dns-query'],
	};
	const tasks = new Map(), choices = {}, probes = { url: {}, tcp: {} };
	let serial = 0;
	const job = (result, stage = 'done', error) => {
		const task = 'job-' + ++serial;
		tasks.set(task, { stage, result, ...(error ? { error } : {}) });
		return { task };
	};
	let logs = [
		{ time: now, source: 'service', level: 'warn', message: 'conflict_services_stopped', detail: 'openclash, passwall2' },
		{ time: now, source: 'service', level: 'info', message: 'running' },
		{ time: now, source: 'core', level: 'error', message: 'network_unreachable', count: 3 },
		{ time: now, source: 'task', level: 'info', message: 'subscription_write/refresh done 0123456789abcdef01234567' },
	];
	let connections = [
		{ id: 'c', created_at: now, device: '192.168.1.23', destination: 'media.example:443', chain: [nodes[0].id, hyperGroup],
			outbound: nodes[0].id, network: 'tcp', rule: 'domain_suffix', result: 'traffic_observed' },
		{ id: 'c-cn', created_at: now, device: '192.168.1.31', destination: 'www.baidu.com:443', chain: ['@direct'],
			outbound: '@direct', network: 'tcp', rule: 'RuleSet,hyper-dns-cn', result: 'traffic_observed' },
	];
	// The Hyper service, as the router relays it (subscription_write api).
	const service = {
		'/v1/public/user/info': { nickname: 'Alex', email: 'user@example.com', refer_code: 'INVITE1', balance: 1200, commission: 300 },
		'/v1/public/user/subscribe': { list: [{
			id: 7, status: 1, subscribe: { name: { 'zh-CN': '标准套餐', 'en-US': 'Standard' }, device_limit: 5 },
			traffic: 2e11, upload: 5e9, download: 37e9, expire_time: now + 30 * 86400e3, start_time: now - 30 * 86400e3,
		}] },
		'/v1/public/user/devices': { list: [
			{ id: 1, current: true, platform: 'OpenWrt', ip: '203.0.113.7', online: true, last_login_at: now - 600e3 },
			{ id: 2, platform: 'iPhone', ip: '198.51.100.20', online: false, last_login_at: now - 86400e3 },
		] },
		'/v1/common/site/config': { currency: { currency_unit: 'USD', currency_symbol: '$' } },
		'/v1/public/user/affiliate/count': { registers: 3, total_commission: 300 },
		'/v1/public/announcement/list': { announcements: [{ id: 1, pinned: true, title: { 'zh-CN': '维护通知', 'en-US': 'Maintenance' },
			content: { 'zh-CN': '今晚维护。', 'en-US': 'Maintenance tonight.' }, created_at: now - 86400e3 }] },
		'/v1/public/subscribe/list': { list: [{
			id: 1, name: { 'zh-CN': '标准套餐', 'en-US': 'Standard' }, description: { 'zh-CN': '五台设备', 'en-US': 'Five devices' },
			unit_time: 'Month', device_limit: 5, traffic: 2e11,
			discount: [{ quantity: 1, sell_price: 999, original_price: 1299, recommend: true }, { quantity: 12, sell_price: 9999 }],
		}], units: {} },
		'/v1/public/payment/methods': { list: [{ id: 1, name: { 'zh-CN': '支付宝', 'en-US': 'Alipay' }, platform: 'alipay', fee_mode: 0 }] },
	};
	async function rpc(m, r = {}) {
		switch (m) {
		case 'status':
			return { saved, phase: saved.enabled && saved.mode !== 'direct' ? 'running' : 'direct', running_since: now - 86400e3 - 3723e3,
				version: '1.0.0', plugin: { installed: '1.0.0-1', candidate: '1.0.1-1', available: true, checked_at: now } };
		case 'subscription_read': {
			const a = r.action;
			if (a === 'list') return { ok: true, data: sources };
			if (a === 'account') return { ok: true, data: signedOut ? { signed_in: false } : { signed_in: true, email: 'user@example.com' } };
			if (a === 'catalog') return { ok: true, data: { items: [
				{ key: 'Streaming', policy: 'PROXY', server_enabled: true, entries: 120, enabled: true },
				{ key: 'Games', policy: 'DIRECT', server_enabled: true, entries: 40, enabled: false },
			] } };
			if (a === 'checkin_status') return { ok: true, data: { result: true, dates: [] } };
			if (a === 'status') return { ok: true, data: sources.find((s) => s.source === r.args.source) };
			if (a === 'set_match') return { ok: true, data: { matched: /\.cn$|baidu\.com$/.test(r.args.query) } };
			if (a === 'profile') return { ok: true, data: { source: 'hyper', format: 'hyper', nodes, groups, rules: [], local: false, final: hyperGroup } };
			if (a === 'rules') {
				const tags = ['list-streaming-domain-proxy', '@hyper-geosite-cn', '@hyper-geoip-cn'];
				const items = tags.map((tag) => ({ match: tag.startsWith('@') ? { clash: 'RULE-SET,' + tag } : { rule_set: tag }, rule_set: tag, target: tag.startsWith('@') ? '@direct' : hyperGroup }));
				return { ok: true, data: { items, total: items.length, positions: items.map((_, i) => i + 1), sets: Object.fromEntries(tags.map((x) => [x, 1])), entries: { 'list-streaming-domain-proxy': 120 }, binary: { 'list-streaming-domain-proxy': true }, formats: { 'list-streaming-domain-proxy': 'text' }, labels: { 'list-streaming-domain-proxy': { list: 'Streaming', kind: 'domain' } } } };
			}
			const rows = a === 'nodes' ? nodes : a === 'groups' ? groups : [];
			return { ok: true, data: { items: rows, total: rows.length } };
		}
		case 'subscription_write':
			if (r.action === 'api') {
				const data = service[r.args.path];
				return job(data ? { ok: true, data: { ok: true, status: 200, code: 200, data } } : { ok: true, data: { ok: false, status: 404, error: 'api_unavailable' } });
			}
			if (r.action === 'checkin') return job({ ok: true, data: { result: true, dates: [new Date().toISOString().slice(0, 10)] } });
			return job({ ok: true, data: { result: true } });
		case 'environment':
			return { firewall4: true, firewall_backend: 'fw4', firewall_engine: 'nftables', firewall_version: '1.1.1', firewall_tproxy: true, firewall_tun: true, free_bytes: 128e6 };
		case 'network_preferences':
			return prefs;
		case 'save_network':
			prefs = { ...prefs, ...r.args };
			return job({});
		case 'devices':
			return { items: [
				{ mac: '02:00:00:01:00:01', name: 'Living room TV', addresses: ['192.168.1.23'], reachability: 'reachable' },
				{ mac: '02:00:00:01:00:02', name: 'Work laptop', addresses: ['192.168.1.31'], reachability: 'reachable' },
			] };
		case 'kernel_read':
			return kernel;
		case 'kernel_write':
			kernel.mode = r.args?.mode || kernel.mode;
			return job({ selected: kernel.selected });
		case 'conflicts':
			return { resources: [], blocked: false };
		case 'rule_hits':
			return { 'list-streaming-domain-proxy': { count: 3, last: now }, 'hyper-dns-cn': { count: 2, last: now } };
		case 'routes':
			return { items: [{ group: hyperGroup, outbound: nodes[0].id, time: now, evidence: 'traffic_observed' }] };
		case 'traffic':
			return { available: true, up_bytes: 7e8, down_bytes: 17.9e9, up_bytes_per_second: 2.1e6, down_bytes_per_second: 38.2e6 };
		case 'health_check':
			return job({ core: { ok: true, delay_ms: 1 }, dns: { ok: true, delay_ms: 4 }, baidu: { ok: true, delay_ms: 32 }, google: { ok: false, delay_ms: 5000 } });
		case 'plugin_check':
			return job({ installed: '1.0.0-1', candidate: '1.0.1-1', available: true });
		case 'groups':
			return choices;
		case 'latencies':
			return probes[r.method || 'url'];
		case 'test_nodes':
			for (const n of r.args.nodes) probes[r.method][n] = { node: n, latency: { measured_at: 100, delay_ms: r.method === 'tcp' ? 12 : 87 } };
			return job({});
		case 'cancel_tests':
			return { cancelled: true };
		case 'live_groups':
			return { items: groups.map((g) => ({ tag: g.id, selected: choices[g.id]?.saved || g.members[0] })) };
		case 'custom_rules':
			return { items: saved.policy.rules || [] };
		case 'logs':
			return { items: logs };
		case 'connections':
			return { items: connections };
		case 'clear':
			if (r.page === 'logs') logs = [];
			else connections = [];
			return { cleared: true };
		case 'task':
			return tasks.get(r.task);
		case 'select_node':
			choices[r.group] = { state: 'applied', saved: r.node, applied: r.node };
			break;
		case 'start': saved.enabled = true; break;
		case 'stop': saved.enabled = false; break;
		case 'policy': saved.policy = r.policy; break;
		case 'save_rules': saved.policy.rules = r.args; break;
		case 'source': saved.source = r.source; break;
		case 'mode': saved.mode = r.mode; break;
		default:
			throw Error('Unexpected fixture operation ' + m);
		}
		return { task: 'intent-' + ++saved.revision, revision: saved.revision };
	}
	return { rpc, saved, kernel };
}
