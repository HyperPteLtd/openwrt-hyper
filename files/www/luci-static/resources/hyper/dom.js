import { t } from './i18n.js';
export function h(tag, attributes = {}, ...children) {
	const el = document.createElement(tag);
	for (const [key, value] of Object.entries(attributes || {})) {
		if (value == null || value === false) continue;
		if (key.startsWith('on') && typeof value === 'function')
			el.addEventListener(key.slice(2).toLowerCase(), value);
		else if (key === 'class' || key === 'className') el.className = value;
		else if (key === 'style' && typeof value === 'object') Object.assign(el.style, value);
		else el.setAttribute(key, value === true ? '' : String(value));
	}
	for (const child of children.flat(Infinity)) {
		if (child != null && child !== false)
			el.append(child instanceof Node ? child : document.createTextNode(String(child)));
	}
	return el;
}
export function card(title, ...children) {
	return h('section', { class: 'ow-card' }, title && h('h2', {}, title), ...children);
}
export function field(label, input) {
	return h(
		input.matches('input,select,textarea') ? 'label' : 'div',
		{ class: 'ow-field' },
		h('span', {}, label),
		input,
	);
}
export function select(label, options, value, change) {
	const el = h(
		'select',
		{ 'aria-label': label },
		options.map(([id, text]) => h('option', { value: id }, text)),
	);
	el.value = value ?? '';
	if (change) el.addEventListener('change', () => change(el.value));
	return el;
}
export const segment = (ui, label, options, value, change) =>
	h(
		'div',
		{ class: 'ow-segment', role: 'group', 'aria-label': label },
		options.map(([id, text]) => {
			const b = ui.button(text, () => change(id));
			b.setAttribute('aria-pressed', String(id === value));
			return b;
		}),
	);
export const empty = (text) => h('p', { class: 'ow-empty' }, text);
// Inline feedback: a line under the row or card whose controls it reports on. A ui.button inside
// that scope writes its error there rather than in the alert at the top of the page, and its next
// press clears the line. The line stays in the accessibility tree while empty, so what is written
// into it is announced: politely, or at once (role alert) for a line about blocking errors, which
// the page clears itself when it retries.
const scopes = new WeakMap();
let lines = 0;
export function feedback(scope, assertive = false) {
	const line = h('p', { class: 'ow-feedback', id: 'ow-feedback-' + ++lines, role: assertive ? 'alert' : 'status' });
	return scope ? attach(scope, line) : line;
}
// A redrawn row keeps its line: the new row reports to the same one.
export function attach(scope, line) {
	scopes.set(scope, line);
	return line;
}
export function feedbackFor(el) {
	for (let node = el; node; node = node.parentElement) if (scopes.has(node)) return scopes.get(node);
	return null;
}
// Text, or text and nodes such as a link; the tone is 'error' or 'done'. The control that
// acted is described by the line.
export function say(line, content, { tone = 'error', control } = {}) {
	line.replaceChildren(...[content].flat());
	line.dataset.tone = tone;
	if (!control) return;
	const ids = (control.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean);
	if (!ids.includes(line.id)) control.setAttribute('aria-describedby', [...ids, line.id].join(' '));
}
export function hush(line) {
	if (!line?.firstChild) return;
	line.replaceChildren();
	delete line.dataset.tone;
}
export function bytes(n) {
	if (n == null || !Number.isFinite(n)) return '—';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	let i = 0;
	while (n >= 1000 && i < units.length - 1) {
		n /= 1000;
		i++;
	}
	return n.toLocaleString(undefined, { maximumFractionDigits: i ? 1 : 0 }) + ' ' + units[i];
}
export function clock(ts) {
	if (!ts) return '—';
	const d = new Date(ts < 1e11 ? ts * 1000 : ts);
	return Number.isNaN(d.getTime()) ? '—' : d.toLocaleTimeString([], { hour12: false });
}
export function download(name, value) {
	const url = URL.createObjectURL(new Blob([value], { type: 'application/json;charset=utf-8' }));
	const link = h('a', { href: url, download: name });
	link.click();
	setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export const modeNames = [
	['smart', t('智能')],
	['global', t('全局')],
	['direct', t('直连')],
];
export const knownNames = {
	'@global': t('全局'),
	'@direct': 'DIRECT',
	'@reject': 'REJECT',
	'@default': t('默认代理'),
};
// The plugin's mainland lists, by the names the core loads them under and its connections
// report. A subscription's GEOSITE,CN and GEOIP,CN rules name them by tag, and so do lookups
// in them; a local profile routes both direct.
export const mainlandSets = {
	'hyper-dns-cn': { label: t('大陆域名'), tag: '@hyper-geosite-cn' },
	'hyper-ip-cn': { label: t('大陆 IP'), tag: '@hyper-geoip-cn' },
};
const mainlandTags = new Map(Object.entries(mainlandSets).map(([name, set]) => [set.tag, name]));
export const isMainland = (name) => Object.hasOwn(mainlandSets, name);
// A rule set's name in the core: a mainland tag reads as its list's.
export const mainlandName = (tag) => mainlandTags.get(tag) || tag;
// The router's other sets its rules match connections by (router config.go): the mainland names
// the proxy must carry, such as Google Play's. They are no mainland list and have no card.
const routerSets = { 'hyper-cn-proxied': t('需代理的大陆域名') };
// A rule set as the pages show it.
// The Hyper account's lists, by the tag of each of their sets (subscription rules' labels).
const listKinds = { domain: t('域名'), keyword: t('域名关键词'), ip: 'IP' };
const listSets = new Map();
export function learnListSets(labels) {
	for (const [tag, l] of Object.entries(labels || {})) listSets.set(tag, l.list + ' · ' + (listKinds[l.kind] || l.kind));
}
export function setLabel(name) {
	if (listSets.has(name)) return listSets.get(name);
	const core = mainlandName(name);
	if (isMainland(core)) return mainlandSets[core].label;
	return Object.hasOwn(routerSets, core) ? routerSets[core] : name;
}
// The rule sets a rule's text names (RULE-SET,name), as the pages show them.
export const setRefs = (text) => text.replace(/(RULE-SET,)([^,()]+)/gi, (_, kind, name) => kind + setLabel(name));
export const resultName = { routed: t('已分流'), established: t('已建立'), failed: t('失败'), closed: t('已关闭') };
export async function pages(ui, source, action) {
	const items = [];
	if (!source) return items;
	for (let offset = 0; ; offset += 256) {
		if (ui.signal?.aborted) throw new DOMException(t('页面已切换'), 'AbortError');
		const { data } = await ui.rpc('subscription_read', { action, args: { source, offset, limit: 256 } });
		items.push(...data.items);
		if (items.length >= data.total || data.items.length < 256) return items;
	}
}

const diagnosticText = {
    // Failed subscription updates, read alike on the subscriptions and log pages.
    subscription_empty: t('订阅内容为空'),
    subscription_web_page: t('服务商返回的是网页（错误、登录或验证页面），不是订阅。请检查订阅链接，或稍后重试'),
    subscription_message: t('服务商返回了一条提示而不是订阅，例如链接过期或流量用尽。请到服务商处查看'),
    unsupported_format: t('无法识别的订阅格式'),
    no_usable_nodes: t('订阅中没有可用的节点'),
    subscription_tls_untrusted: t('路由器不信任此订阅服务的 HTTPS 证书，请检查固件的 CA 证书和服务商的证书链'),
    subscription_link_unavailable: t('服务商未提供可用的套餐订阅链接，或链接的下载地址拒绝了请求，请联系服务商'),
    subscription_link_unreachable: t('套餐订阅链接暂时无法下载（连接中断、超时或服务器繁忙），请稍后重试'),
    invalid_plan_detail: t('服务商返回的套餐详情不完整或与当前套餐不匹配'),
    starting: t('正在启动代理'),
    releasing: t('正在清理接管规则'),
    fail_open: t('局域网已回退直连'),
    protected: t('故障保护已生效'),
    release_failed: t('网络恢复未完成'),
    running: t('代理已启动'),
    mode: t('切换代理模式'),
    start: t('请求启动代理'),
    stop: t('手动停止代理'),
    source: t('切换订阅'),
    policy: t('修改代理策略'),
    save_rules: t('保存分流规则'),
    uninstall: t('准备卸载'),
    node_failover_requested: t('当前节点不可用，已请求切换节点'),
    firewall_rules_restored: t('防火墙重启或其他程序清除了代理规则，已自动恢复'),
    conflict_services_stopped: t('已停用冲突的代理插件'),
    connections_not_reset: t('未能断开旧连接，已打开的页面可能仍走原线路，刷新或重开即可'),
    kernel_bootstrap_marker_invalid: t('首次内核准备标记无效，已忽略'),
    kernel_bootstrap_queue_failed: t('首次内核准备未能加入任务队列'),
    rule_provider_cleanup_failed: t('清理未使用的规则集失败'),
    // Why an update a refresh downloaded was not applied (task stage held).
    confirmation_required: t('节点有变更待确认，请到账户页查看并确认'),
    update_blocked: t('同步到的新版本无法使用，已保留上次的节点，可到账户页查看原因'),
    initial_subscription_select_failed: t('无法选择初始订阅'),
    device_id_failed: t('无法生成本机设备标识'),
    legacy_settings_failed: t('旧版本设置迁移失败，已使用默认设置'),
    superseded: t('已被更新的任务取代'),
    task_journal_write_failed: t('任务记录写入失败'),
    china_lists_update_failed: t('大陆域名与 IP 列表多次更新失败，继续使用现有列表'),
    network_unreachable: t('内核发起连接时，系统报告网络不可达'),
    connection_refused: t('目标端拒绝了连接'),
    dns_name_not_found: t('DNS 查询未找到域名'),
    tls_certificate_error: t('TLS 证书校验失败'),
    io_timeout: t('连接读写超时'),
    deadline_exceeded: t('操作超过时限'),
    core_diagnostic: t('内核报告了一条其他消息（原文可能含地址或域名，未记录）'),
    connection_reset: t('连接被对端重置'),
    connection_closed: t('连接被对端提前关闭'),
    geodata_unavailable: t('GeoIP / GeoSite 数据读取或下载失败'),
    rule_provider_issue: t('规则集加载或更新失败'),
    node_provider_issue: t('节点提供方加载或更新失败'),
    sniffer_issue: t('域名嗅探出错'),
    core_tun_issue: t('TUN 设备或路由设置出错'),
    dial_failed: t('内核连接目标失败'),
    dns_query_failed: t('DNS 查询失败'),
    listen_address_in_use: t('内核监听地址已被占用'),
    core_listener_failed: t('内核无法在配置的地址上监听'),
    disk_full: t('可写存储空间已满'),
    permission_denied: t('内核报告权限不足，展开查看被拒绝的操作'),
    core_tproxy_socket_permission: t('内核没有权限设置透明代理套接字'),
    core_tproxy_udp_reply_permission: t('内核没有权限绑定低端口发送 UDP 回包，QUIC 等流量会丢包'),
    core_icmp_socket_permission: t('内核没有权限使用 ICMP 套接字'),
    core_listener_permission: t('内核没有权限绑定监听端口'),
    core_route_mark_permission: t('内核没有权限设置套接字路由标记'),
    core_thread_creation_failed: t('内核无法创建工作线程'),
    core_out_of_memory: t('路由器内存不足，内核无法再分配内存，可能已退出'),
    core_file_limit_reached: t('内核打开的连接和文件数达到上限（too many open files），新连接会失败或被断开'),
    core_file_limit_low: t('无法把内核的文件描述符上限提高到 1000000，连接较多时可能出现“too many open files”'),
    core_outbound_blocked: t('内核的出站连接被系统拒绝，常见原因是防火墙拦截了路由器自身发出的流量'),
    core_process_lookup_permission: t('内核无法读取进程信息，按进程匹配的规则不会生效'),
    core_file_permission: t('内核没有权限访问某个文件'),
    handshake_failed: t('连接握手失败'),
    invalid_configuration: t('内核拒绝了配置'),
    dns_default_upstream: t('存在 Hyper 无法自动接管的默认上游或全域 DNS 规则，通常来自其他插件的配置文件。请将它改为按域名转发，或关闭 Hyper 的 DNS 保护'),
    dns_servers_file: t('servers-file 可在运行时更换上游，请检查该文件或关闭 Hyper 的 DNS 保护'),
    dns_dynamic_config: t('conf-script 动态生成 DNS 配置，无法确认上游。可关闭 Hyper 的 DNS 保护'),
    dns_no_instance: t('找不到为所选局域网接口提供解析的 dnsmasq 实例'),
    dns_include_directory: t('需要一个由此局域网使用、可加载 .conf 文件的临时 conf-dir'),
    dns_shared_instance: t('此 dnsmasq 实例同时服务受管与其他网络，请拆分实例或关闭 Hyper 的 DNS 保护'),
    dns_config_unreadable: t('无法读取 dnsmasq 实例及其引用的配置，请检查 conf-file 和 conf-dir'),
    dns_file_collision: t('dnsmasq 已有同名上游配置，请检查 99-hyper-upstream.conf 的归属'),
    'network apply failed at fault_flow_offload': t('防火墙已启用流量分载，现有连接可能绕过故障保护。请关闭防火墙流量分载，或启用故障时回退直连'),
    'network apply failed at service_stopping': t('正在停止服务，本次接管已取消'),
    'network apply failed at dns_only_firewall': t('仅 DNS 保护需要 firewall4（nftables）防火墙'),
    kernel_validation_timeout: t('内核配置校验超时'),
    kernel_storage_reserve: t('剩余空间不足以保存配置和恢复预留空间'),
    kernel_storage_full: t('保存内核状态时空间已满'),
    kernel_permission_denied: t('读取内核或保存配置时权限不足'),
    kernel_selection_changed: t('内核版本正在切换，请在当前任务完成后重试'),
    kernel_integrity_check_failed: t('已安装内核的文件大小或 SHA-256 校验失败'),
    kernel_config_unreadable: t('无法读取生成的配置文件'),
    kernel_file_missing: t('缺少启动所需的内核或配置文件'),
    kernel_not_installed: t('请先在内核管理中安装并选择一个版本'),
    kernel_restore_unavailable: t('无法恢复内存中的内核，请重新安装插件'),
    kernel_preparation_failed: t('内核准备失败，未取得更具体的错误分类'),
    // Why a subscription operation failed (hyper-subscription's error codes), on the
    // subscriptions page and in the log.
    subscription_request_failed: t('无法连接订阅服务器，请检查网络后重试'),
    invalid_response_size: t('服务器返回的内容超过大小上限'),
    subscription_incomplete: t('与订阅服务器的连接中断或超时，内容未完整下载，请稍后重试'),
    subscription_redirect_refused: t('订阅服务器要求跳转，但跳转不符合下载要求，已拒绝。请检查订阅链接或联系服务商'),
    subscription_size_limit: t('订阅内容超过大小上限'),
    expanded_subscription_limit: t('展开引用的节点和规则后，订阅超过大小上限'),
    invalid_subscription: t('无法解析订阅内容'),
    update_failed: t('订阅更新失败'),
    operation_failed: t('订阅操作失败'),
    context_deadline_exceeded: t('操作超时'),
    not_found: t('找不到这个订阅'),
    not_ready: t('订阅尚未就绪，请先更新订阅'),
    login_required: t('登录已失效，请重新登录 Hyper 账户'),
    suspended: t('套餐已到期或不可用，续费后再同步'),
    platform_account_revoked: t('服务商账户已停用'),
    subscription_app_disabled: t('服务商已停止向此客户端提供订阅，请联系服务商'),
    entitlement_check_failed: t('无法向服务商确认套餐状态，请稍后重试'),
    invalid_plan_list: t('服务商返回的套餐列表无效'),
    invalid_node_inventory: t('服务商返回的节点清单无效'),
    invalid_platform_site: t('这个地址不是受支持的服务商站点'),
    invalid_origin: t('服务商站点地址无效'),
    invalid_login: t('请填写有效的邮箱和密码'),
    invalid_login_response: t('服务商返回的登录结果无效'),
    invalid_login_token: t('服务商返回的登录凭据无效'),
    verified_login_required: t('请先登录服务商账户'),
    source_identity_mismatch: t('订阅类型或服务商账户与原订阅不一致'),
    redirect_refused: t('服务器的跳转不符合下载要求，已拒绝'),
    authenticated_redirect_refused: t('带登录凭据的请求不能跳转到其他站点，已拒绝'),
    candidate_validation_failed: t('新配置未通过校验，已保留上次配置'),
    preflight_revision_missing: t('新配置未完成校验，请重试'),
    preflight_revision_changed: t('校验期间配置已变更，请重试'),
    preflight_confirmation_timeout: t('确认新配置超时，请重试'),
    invalid_candidate: t('新配置无效'),
    kernel_invalidation_pending: t('订阅已撤销，内核尚未停用它的配置，稍后会自动重试'),
    invalid_request: t('请求无效'),
    unknown_method: t('不支持的订阅操作'),
    invalid_page: t('分页参数无效'),
    invalid_source: t('订阅设置无效'),
    invalid_source_kind: t('订阅类型无效'),
    invalid_source_url: t('订阅链接无效，请填写 http 或 https 地址'),
    invalid_external_source: t('链接订阅的设置无效'),
    invalid_file_source: t('本地文件订阅的设置无效'),
    invalid_platform_source: t('服务商订阅的设置无效'),
    invalid_template_filter: t('模板筛选条件无效'),
    source_config_required: t('缺少订阅设置'),
    source_preferences_required: t('缺少要修改的订阅设置'),
    external_source_required: t('只有链接订阅可以更换链接'),
    not_external: t('这不是链接订阅'),
    invalid_provider_choice: t('所选下载线路无效'),
    proxy_not_core: t('代理端口不属于内核，已停止经由它下载'),
    proxy_connect_refused: t('内核代理拒绝了下载连接'),
    socket_group_unavailable: t('无法建立绕过代理的下载连接'),
    literal_address_required: t('绕过代理下载需要 IP 地址'),
    template_rule_set_unavailable: t('内置分流规则引用的规则集暂时无法取得'),
    template_cache_write_failed: t('无法保存内置分流规则的本机副本'),
    geodata_download_failed: t('GeoIP / GeoSite 数据下载失败'),
    geodata_digest_mismatch: t('GeoIP / GeoSite 数据校验失败'),
    invalid_binary_set_config: t('规则集设置无效'),
    binary_set_digest_mismatch: t('规则集文件校验失败'),
    invalid_cached_rule_file: t('本机缓存的规则集文件无效'),
    invalid_rule_set_format: t('规则集格式无效'),
    invalid_rule_directory: t('规则集目录无效'),
    invalid_rule_directory_owner: t('规则集目录的属主不正确'),
    rule_file_size_exceeds_limit: t('规则文件超过大小上限'),
    invalid_set_query: t('请输入域名或 IP 地址'),
    snapshot_exceeds_storage_limit: t('订阅数据超过存储上限'),
    invalid_subscription_storage: t('订阅存储目录无效'),
    invalid_subscription_state: t('订阅状态文件无效'),
    invalid_subscription_snapshot: t('订阅快照文件无效'),
    // Import notices the subscriptions page reads here (noticeRows).
    invalid_endpoint: t('节点的地址、端口或名称无效'),
    ambiguous_group: t('策略组与其他节点或策略组同名，对它的引用无法使用'),
    mihomo_unavailable_member: t('策略组的部分成员不存在或不可用，已移除'),
    invalid_group: t('策略组没有可用成员或配置无效，已跳过'),
    cyclic_group: t('策略组之间形成循环引用'),
    unresolved_target: t('规则指向的节点或策略组不存在，已跳过这条规则'),
    unsupported_group_option: t('策略组有路由器无法使用的选项，已忽略'),
    // A node the page chose that the current kernel cannot use (select_node).
    mihomo_protocol_unsupported: t('当前内核无法使用该协议，请切换到支持它的版本'),
    // The Hyper account (subscription helper: hyper methods and the hyper source).
    subscription_token_unreachable: t('暂时无法连接 Hyper 服务，请检查网络后重试'),
    locations_unreachable: t('暂时无法连接 Hyper 服务，请检查网络后重试'),
    subscription_token_failed: t('Hyper 服务拒绝了订阅请求，请确认套餐是否有效'),
    locations_failed: t('Hyper 服务拒绝了节点列表请求，请稍后重试'),
    nodes_unreachable: t('所有地区的节点都没能读取，已保留上次的节点，请稍后重试'),
    not_signed_in: t('尚未登录 Hyper 账户'),
    no_subscription: t('账户还没有可用的套餐'),
    no_countries: t('账户下暂时没有可用的节点'),
    'not signed in': t('尚未登录 Hyper 账户'),
    account_unavailable: t('账户服务不可用，请重启 Hyper 服务后重试'),
    'missing credentials': t('请输入邮箱和验证码'),
    'illegal email': t('请输入正确的邮箱地址'),
    'illegal method': t('请求方法不允许'),
    'path not allowed': t('路由器不允许访问这个账户接口'),
    'idempotency key required': t('支付请求缺少防重复标识，请刷新页面后重试'),
    'illegal date': t('签到日期无效，请检查路由器时间'),
    'write failed': t('无法保存签到记录，请检查路由器存储空间'),
    invalid_hyper_source: t('账户订阅配置无效'),
    invalid_rule_toggle: t('规则开关请求无效'),
};

const taskText = {
    'subscription_write/refresh': t('更新订阅'),
    'kernel/bootstrap': t('首次准备内核'),
    save_network: t('保存网络设置'),
    health_check: t('连通性检测'),
    plugin_check: t('检查插件更新'),
    plugin_update: t('更新插件'),
    plugin_auto: t('自动检查插件更新'),
    subscription_write: t('订阅'),
    kernel_read: t('读取内核'),
    kernel: t('内核'),
    configure: t('应用配置'),
    latency: t('节点测速'),
};
const taskStep = {
    configure: t('修改设置'), login: t('登录'), logout: t('退出登录'), site: t('获取站点信息'),
    send_code: t('发送验证码'), api: t('读取账户信息'), checkin: t('签到'), rule_toggle: t('切换服务端规则'),
    bundled: t('安装随包内核'),
    preferences: t('修改偏好'), rollback: t('回滚'), revoke: t('撤销授权'),
    url: t('URL 测速'), tcp: t('TCP 测速'), versions: t('获取版本列表'), 'install-json': t('安装'), select: t('切换版本'),
};
const taskStage = { done: t('完成'), failed: t('失败'), held: t('未应用') };
function taskName(action) {
    if (taskText[action]) return taskText[action];
    const [head, ...tail] = action.split('/');
    const rest = tail.join('/');
    const name = taskStep[rest] || diagnosticText[rest] || rest;
    return taskText[head] ? taskText[head] + (rest ? ' · ' + name : '') : action;
}

export function datetime(ts) {
    if (!ts) return '—';
    const d = new Date(typeof ts === 'number' && ts < 1e11 ? ts * 1000 : ts);
    if (Number.isNaN(d.getTime())) return '—';
    const p = (n) => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' +
        p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}
export const day = (ts) => datetime(ts).slice(0, 10);

const errorText = {
    'active subscription unavailable': t('当前订阅不可用'),
    'candidate has unavailable source references': t('配置引用了不可用的订阅'),
    'cannot launch core': t('无法启动内核'),
    'cannot prepare authorized rule sets': t('无法准备规则集文件'),
    'capability information does not match selected release': t('内核能力信息与所选版本不符'),
    'capability revision mismatch': t('内核能力信息版本不符'),
    'choose at least one LAN network': t('请至少选择一个局域网'),
    'configuration revision changed': t('配置已被其他操作修改，请刷新后重试'),
    'control unavailable': t('内核控制接口不可用'),
    'core credential unavailable': t('内核控制凭据不可用'),
    'core did not confirm saved group selection': t('内核未确认已保存的节点选择'),
    'core exited during startup': t('内核在启动过程中退出'),
    'core group unavailable': t('无法读取内核运行用户组 nogroup'),
    'core selection unavailable': t('无法读取内核的节点选择'),
    'could not save network preferences': t('无法保存网络设置'),
    'verified plugin update failed': t('插件更新失败'),
    'A package update is already in progress': t('已有插件更新正在进行'),
    'APK identity does not match release': t('下载的 APK 与发布信息不一致'),
    'IPK identity does not match release': t('下载的 IPK 与发布信息不一致'),
    'Cannot compare package versions': t('无法比较插件版本'),
    'Cannot download a package matching the release digest': t('无法下载与发布校验值一致的安装包，请检查网络或下载镜像'),
    'Cannot plan the offline APK upgrade': t('无法规划 APK 升级，可能缺少依赖'),
    'Cannot read the router architecture': t('无法读取路由器架构'),
    'Cannot verify the official release metadata': t('无法验证官方发布信息'),
    'Checking plugin updates requires the ucode command': t('检查更新需要 ucode 命令'),
    'Checking plugin updates requires an HTTPS downloader (uclient-fetch, curl or GNU Wget)': t('检查更新需要 HTTPS 下载工具（uclient-fetch、curl 或 GNU Wget）'),
    'Installed APK providers changed during preflight': t('升级检查期间已安装的组件发生变化，请重试'),
    'Insufficient temporary storage for update': t('临时空间不足，无法更新'),
    'Invalid installed package version': t('已安装的插件版本无效'),
    'Package manager did not apply the requested version': t('包管理器没有安装目标版本'),
    'Refusing package downgrade': t('不允许降级插件'),
    'invalid network preferences': t('设置无效'),
    'could not verify network prerequisites': t('无法确认网络前置条件'),
    'default route has no available group': t('默认路线没有可用的策略组'),
    'invalid control credential': t('内核控制凭据无效'),
    'invalid core group': t('内核运行用户组 nogroup 无效（不能是 root 组）'),
    'invalid launch ticket': t('内核启动凭证无效'),
    'invalid managed LAN': t('受管局域网无效'),
    'invalid private core work directory': t('内核工作目录无效'),
    'invalid rule provider digest': t('规则集文件校验失败'),
    'local core control unavailable': t('本地内核控制接口不可用'),
    'local core DNS answer unavailable': t('内核 DNS 没有应答'),
    'local proxy listener unavailable': t('内核代理端口不可用'),
    'local proxy negotiation failed': t('内核代理握手失败'),
    'local TUN not ready': t('TUN 设备未就绪'),
    'managed LAN has no usable address': t('受管局域网没有可用地址'),
    'managed LAN interface unavailable': t('受管局域网接口不可用'),
    'missing control secret': t('缺少内核控制密钥'),
    'network ownership lost': t('网络接管已被其他程序修改'),
    'network resource conflict or incomplete inspection': t('网络资源冲突，或检查未完成'),
    'no authorized primary source': t('没有可用的主订阅'),
    'no compatible general-purpose nodes on this release': t('当前内核版本不支持订阅中的任何节点'),
    'no managed LAN': t('没有受管局域网'),
    'policy exceeds router limits': t('代理策略超出路由器限制'),
    'previous confirmed kernel available for recovery': t('可回退到上一个确认可用的内核'),
    'referenced node no longer exists': t('所选节点已不存在'),
    'referenced primary node no longer exists': t('所选主节点已不存在'),
    'referenced subscription unavailable': t('所选订阅不可用'),
    'revoked': t('订阅授权已撤销'),
    'route check returned an error': t('路由检查出错'),
    'router intent changed during validation': t('校验期间配置已变更'),
    'rule provider name collision': t('规则集名称冲突'),
    'selected release lacks the configured interception method': t('所选内核版本不支持当前的接管方式'),
    'source has changed': t('订阅内容已变更'),
    'state not ready': t('服务状态尚未就绪'),
    'subscription authorization changed': t('订阅授权已变更'),
    'subscription changed during startup': t('启动过程中订阅已变更'),
    'transparent listeners not ready': t('透明代理端口未就绪'),
    'unexpected DNS canary response': t('DNS 探测返回了意外结果'),
    'unsafe core work directory': t('内核工作目录权限不安全'),
    'unsupported core control interface': t('不支持的内核控制接口'),
    'unsupported network method': t('不支持的接管方式'),
    'unsupported rule provider format': t('不支持的规则集格式'),
    'unsupported state schema': t('不支持的状态文件版本'),
    'unverified network fault disposition': t('未确认故障时的网络处理方式'),
    'context deadline exceeded': t('操作超时'),
    'context canceled': t('操作已取消'),
    'operation already in progress': t('已有相同的操作正在进行，请稍后重试'),
    'manager restarted before completion': t('任务完成前服务已重启'),
    'operation failed': t('操作失败'),
    'response too large': t('辅助程序返回的内容过大'),
    'invalid helper response': t('辅助程序返回了无效结果'),
    'core is not running': t('代理未运行'),
    'candidate core rejected the configuration': t('内核拒绝了此配置'),
    'no compatible members on this device': t('没有当前内核可用的成员'),
    'saved member unavailable on this device': t('保存的节点在当前内核中不可用'),
    // What a start or a kernel check fails with besides: the configuration the router compiles,
    // the core's control interface and the router's own commands.
    "cannot reclaim the core's runtime directory": t('无法清理内核的运行目录'),
    'unexpected path in core home': t('内核数据目录中有无法识别的文件'),
    'unknown routing mode': t('未知的代理模式'),
    'unknown interception method': t('未知的接管方式'),
    'invalid or duplicate node identity': t('订阅中有无效或重复的节点标识'),
    'duplicate group identity': t('订阅中有重复的策略组标识'),
    'duplicate local group identity': t('自定义策略组的标识重复'),
    'too many local groups': t('自定义策略组过多'),
    'invalid local group': t('自定义策略组无效'),
    'invalid explicit source/node reference': t('所选订阅或节点的引用无效'),
    'expected a device MAC or IP prefix': t('设备须以 MAC 地址或 IP 段指定'),
    'invalid native rule': t('分流规则无效'),
    'empty rule condition': t('分流规则的条件为空'),
    'empty or invalid rule condition': t('分流规则的条件为空或无效'),
    'invalid rule operand': t('分流规则的条件无效'),
    'invalid rule value': t('分流规则的匹配值无效'),
    'invalid IP version': t('分流规则的 IP 版本无效'),
    'invalid Telegram LAN listener': t('Telegram 的局域网监听设置无效'),
    'invalid Telegram LAN permission': t('Telegram 的局域网放行设置无效'),
    'Telegram listener must use an address of a managed LAN': t('Telegram 的监听地址必须属于受管局域网'),
    'Telegram address is not assigned to managed interface': t('Telegram 的监听地址不在受管接口上'),
    'Telegram client range outside managed LAN': t('Telegram 的客户端范围超出受管局域网'),
    'Telegram range outside managed LAN': t('Telegram 的地址范围超出受管局域网'),
    'Telegram source no longer available': t('Telegram 使用的订阅已不可用'),
    'missing local control credential': t('缺少内核控制凭据'),
    'missing controller proxies': t('内核没有返回节点列表'),
    'oversized local control response': t('内核控制接口返回的内容过大'),
    'invalid control object name': t('节点或策略组的名称无效'),
    'empty core version': t('内核没有返回版本号'),
    'empty traffic sample': t('内核没有返回流量数据'),
    'core did not confirm selection': t('内核未确认节点切换'),
    'node probe failed': t('节点测速失败'),
    // What an action fails with, shown where it was.
    'uninstall in progress': t('正在卸载'),
    'source unavailable': t('订阅不可用'),
    'subscription unavailable': t('订阅不可用'),
    'source required': t('请选择订阅'),
    'source already refreshing': t('这个订阅正在更新'),
    'unknown selectable member': t('所选节点已不在策略组中，请刷新后重试'),
    'invalid selection': t('所选节点无效'),
    'subscription operation failed': t('订阅操作失败'),
    'newer configuration accepted': t('已有更新的设置，本次修改未保存'),
    'invalid custom rules': t('分流规则无效'),
    'invalid custom rule': t('分流规则无效'),
    'invalid or duplicate custom rule identifier': t('分流规则的标识无效或重复'),
    'custom rule note exceeds 256 characters': t('分流规则的备注超过 256 个字符'),
    'unsupported custom rule condition': t('不支持的分流规则条件'),
    'invalid device route': t('设备的分流设置无效'),
    'invalid subscription refresh period': t('订阅更新周期无效'),
    'invalid subscription refresh hour': t('订阅每日更新时间无效'),
    'invalid core log level': t('日志级别无效'),
    'storage mode must be double, single or tmp': t('内核存储位置无效'),
    'choose one to four DNS upstreams': t('请选择一到四个 DNS 上游'),
    'invalid DNS upstream': t('DNS 上游无效'),
    'invalid or duplicate LAN network': t('局域网无效或重复'),
    'configuration intent has changed': t('配置已被其他操作修改，请刷新后重试'),
    'configuration changed during preflight': t('校验期间配置已变更，请重试'),
    'manager is starting or unavailable': t('服务正在启动或不可用，请稍后重试'),
    'network ownership must first be released': t('请先停止代理再进行此操作'),
    'invalid candidate': t('新配置无效'),
    'invalid candidate snapshot': t('新配置无效'),
    'source preflight failed': t('新订阅未通过校验'),
    'source preflight expired': t('新订阅的校验已过期，请重试'),
    'choose between 1 and 4096 imported nodes': t('请选择 1 到 4096 个节点'),
    'invalid or duplicate node': t('所选节点无效或重复'),
    'invalid node identifier': t('节点标识无效'),
    'node no longer belongs to the active configuration': t('节点已不在当前配置中，请刷新后重试'),
    'active source changed or core stopped': t('当前订阅已切换或代理已停止'),
    'unrecognized task journal; state retained': t('无法识别任务记录，已保留原文件'),
    'invalid refresh request': t('更新请求无效'),
    'invalid kernel request': t('内核请求无效'),
    'invalid version': t('版本无效'),
    'unsupported kernel request field': t('内核请求包含不支持的字段'),
    'RPC body too large': t('请求内容过大'),
    'expected an RPC object': t('请求无效'),
    'unknown intent': t('未知的任务'),
    'unknown task': t('找不到这个任务'),
    'unknown log type': t('未知的日志类型'),
    'unknown probe method': t('不支持的测速方式'),
    'unknown kernel operation': t('不支持的内核操作'),
    'unknown kernel query': t('不支持的内核查询'),
    'unknown read operation': t('不支持的读取操作'),
    'unknown subscription operation': t('不支持的订阅操作'),
    'unknown configuration operation': t('不支持的设置操作'),
    'unknown operation': t('不支持的操作'),
};
// Why a referenced node or rule file could not be downloaded (provider_diagnostic.go), after
// the provider's kind and file on the subscriptions page.
export const providerErrors = {
    provider_timeout: t('请求超时'), provider_dns_failed: t('无法解析提供方域名'),
    provider_tls_untrusted: t('HTTPS 证书校验失败'), provider_connection_refused: t('提供方拒绝连接'),
    provider_unreachable: t('无法连接提供方'), private_provider_address: t('提供方指向私有或本地地址，已阻止访问'),
    provider_redirect_refused: t('提供方重定向不符合下载策略'), invalid_provider_url: t('提供方下载地址无效'),
    provider_limit: t('引用的提供方数量超过上限'), provider_size_limit: t('提供方文件超过下载上限'), provider_incomplete: t('文件传输不完整'),
    provider_cancelled: t('下载已取消'), provider_download_failed: t('引用文件下载失败'),
    invalid_proxy_provider: t('节点文件格式无效'), invalid_rule_provider: t('规则文件格式无效'),
    node_limit: t('节点数量超过上限'), rule_limit: t('规则数量超过上限'),
};
// The kernel's errors, read alike on the kernel page and in the log: what the kernel helper
// reports through hyperd (publicKernelError), what the kernel's jobs fail with, and a helper
// command the router ran that failed without a reason.
const kernelErrors = {
    'release checksum mismatch': t('下载的文件未通过校验，请稍后重试'),
    'upstream release checksum unavailable': t('暂时无法取得官方校验值，请稍后重试'),
    'release archive incompatible with this router': t('这个版本不支持此路由器'),
    'release executable failed version validation': t('内核未能在此路由器上运行'),
    'insufficient resources for this storage mode': t('存储空间或内存不足'),
    'release source unreachable': t('无法连接下载地址，请检查网络后重试'),
    'release source temporarily unreachable': t('下载地址暂时不可用，请稍后重试'),
    'GitHub release API rate limit reached': t('GitHub 查询次数已达上限，请稍后重试'),
    'latest stable release unreachable': t('无法获取最新版本，请检查网络后重试'),
    'publisher has no stable release': t('暂时没有可用的正式版'),
    'invalid latest stable release identity': t('最新版本信息未通过校验'),
    'latest stable release lacks a verified archive for this router': t('最新版本没有适用于此路由器的文件'),
    'kernel operation failed': t('内核操作失败'),
    'kernel selection revision changed': t('内核版本已被其他操作切换，请刷新后重试'),
    'router intent changed; version was not selected': t('配置在此期间已变更，未切换内核版本，请重试'),
    'no retained version available': t('没有可回退的内核版本'),
    'missing primary source for candidate validation': t('缺少主订阅，无法校验新内核'),
    'service stopped before the kernel change was confirmed': t('内核切换确认前代理已停止'),
    'invalid initial kernel marker': t('首次内核准备标记无效'),
    'hyper-kernel status failed': t('读取内核状态失败'),
    'hyper-kernel capabilities failed': t('读取内核能力信息失败'),
    'hyper-kernel recover failed': t('恢复内核失败'),
    'hyper-kernel closed failed': t('内核退出后的清理失败'),
    'hyper-kernel reject failed': t('退回上次确认的内核版本失败'),
    'hyper-kernel begin failed': t('准备启动内核失败，未取得具体原因'),
    'hyper-kernel confirm failed': t('确认新内核失败'),
    'hyper-kernel abort failed': t('结束内核试运行失败'),
    'hyper-kernel cancel failed': t('结束内核试运行失败'),
    'hyper-kernel check failed': t('内核校验配置失败，未取得具体原因'),
};
// The name of a node or group by its ID, when the page has read the subscription (app.js).
let memberName = () => '';
export function nameMembers(name) {
    memberName = name;
}
// The objects the kernel's rejections name besides a node or group (kernelcli rejection).
const rejectedEntries = { proxy: t('某个节点'), 'proxy group': t('某个策略组'), rule: t('某条规则'), rules: t('某条规则') };
// The proxy plugins Hyper stops when it starts, by their init script names.
const pluginNames = {
    openclash: 'OpenClash', passwall: 'PassWall', passwall2: 'PassWall 2', homeproxy: 'HomeProxy',
    v2raya: 'v2rayA', shadowsocksr: 'ShadowSocksR Plus+', nikki: 'Nikki', momo: 'Momo',
};
export const pluginName = (id) => pluginNames[id] || id;
const conflictKinds = {
    service: t('服务'), process: t('进程'), nft: 'nftables', xtables: 'iptables', route: t('策略路由'), listener: t('端口'),
    dnsmasq: t('dnsmasq 配置'), interface: t('网络接口'), autostart: t('无法关闭开机自启'), unverified: t('无法检查'),
};
// One thing the conflict scan found holding the network.
export function conflictItem(kind, name) {
    if (kind === 'service' || kind === 'autostart') name = pluginName(name);
    return conflictKinds[kind] ? conflictKinds[kind] + ' ' + name : name;
}
// What the network helper was doing when it failed.
const networkActions = { apply: t('网络接管'), verify: t('网络检查'), release: t('网络恢复'), repair: t('网络修复'), refresh: t('网络更新'), fault: t('网络故障处理') };
const errorPatterns = [
    [/^local health: (.+)$/s, (m) => t('内核健康检查连续失败 · {0}', errorMessage(m[1]))],
    [/^local control unavailable: (.+)$/s, () => t('本地控制接口不可用')],
    [/^(.+) \(release failed: (.+)\)$/s, (m) => t('{0} · 清理接管规则也失败：{1}', errorMessage(m[1]), errorMessage(m[2]))],
    [/^network takeover blocked by: (.+)$/s, (m) => t('仍有无法自动处理的冲突：{0}', m[1].split('; ').map((item) => {
        const [kind, ...name] = item.split(' ');
        return conflictItem(kind, name.join(' '));
    }).join(t('、')))],
    [/^member unavailable in (.+)$/s, (m) => t('策略组 {0} 中的所选节点不可用', m[1])],
    [/^saved member unavailable for (.+)$/s, (m) => t('策略组 {0} 保存的节点不可用', m[1])],
    [/^unavailable (?:device|rule) target (.+)$/s, (m) => t('规则指向的策略组 {0} 不可用', m[1])],
    [/^unsupported group type (.+)$/s, (m) => t('不支持的策略组类型 {0}', m[1])],
    // A change the core could not apply in place; it restarts instead.
    [/^reload fallback (.+)$/s, (m) => t('无法直接应用更改，已重启内核 · {0}', errorMessage(m[1]))],
    // What the kernel rejected: a generated node or group, an entry of the configuration or the
    // whole configuration.
    [/^(node|group):([0-9a-f]{32}): (.+)$/s, (m) => {
        const name = memberName(m[1] + ':' + m[2]), node = m[1] === 'node';
        return (name ? (node ? t('节点 ') : t('策略组 ')) + name : node ? t('某个节点') : t('某个策略组')) + ' · ' + errorMessage(m[3]);
    }],
    [/^(configuration|proxy|proxy group|rules?)(?:\[\d+\])?: (.+)$/s, (m) => (rejectedEntries[m[1]] ? rejectedEntries[m[1]] + ' · ' : '') + errorMessage(m[2])],
    [/^candidate core rejected (\S+) configuration$/, (m) => t('内核拒绝了 {0} 配置', m[1])],
    [/^subscription_http_([1-5][0-9]{2})$/, (m) => t('订阅服务器返回 HTTP {0}', m[1])],
    [/^provider_http_([1-5][0-9]{2})$/, (m) => t('节点或规则提供方返回 HTTP {0}', m[1])],
    [/^(?:subscription|provider)_tls_([a-z_]+)$/, (m) => tlsMessage({ reason: m[1] }) || t('HTTPS 证书校验失败')],
    // The certificate check a kernel download failed, with its validity dates (tlsdiag Wire).
    [/^release_tls (\{.*\})$/, (m) => {
        try { return tlsMessage(JSON.parse(m[1])) || t('HTTPS 证书校验失败'); } catch { return t('HTTPS 证书校验失败'); }
    }],
    // A kernel change the service did not confirm (waitForKernel).
    [/^candidate failed startup or health checks; previous kernel (\S+) is running$/, (m) => t('新内核未通过启动或健康检查，已恢复上一版 {0}', m[1])],
    [/^kernel change unconfirmed: (.*)$/s, (m) => t('未能确认内核切换{0}', m[1] ? ' · ' + diagnosticMessage(m[1]) : '')],
    // Errors joined (errors.Join), one per line.
    [/^([^\n]+)\n(.+)$/s, (m) => errorMessage(m[1]) + ' · ' + errorMessage(m[2])],
    // A start that timed out, with the check that failed last (startupTimeout).
    [/^(.+) \(last failed check: (.+)\)$/s, (m) => t('{0} · 最后一次检查：{1}', errorMessage(m[1]), errorMessage(m[2]))],
    [/^core rejected the new configuration: (.+)$/s, (m) => t('内核拒绝了新配置 · {0}', errorMessage(m[1]))],
    [/^network prerequisites unavailable: /, () => t('路由器缺少接管所需的功能，请到概览页查看需要处理的项目')],
    // A command the router ran that failed without a reason: the network helper, or another.
    [/^([\w.-]+) ([\w-]+) failed$/, (m) => m[1] === 'network'
        ? t('{0}失败', networkActions[m[2]] || t('网络处理'))
        : t('命令 {0} {1} 执行失败', m[1], m[2])],
    [/^condition (\S+) cannot be expressed in Mihomo$/, (m) => t('分流规则的条件 {0} 无法转换为内核规则', m[1])],
    // A file operation the system refused, without its path (os.PathError, os.LinkError).
    [/: (read-only file system|no space left on device|permission denied|operation not permitted|input\/output error|no such file or directory)$/, (m) => ({
        'read-only file system': t('存储为只读，无法写入'), 'no space left on device': t('存储空间已满'),
        'permission denied': t('没有访问所需文件的权限'), 'operation not permitted': t('系统不允许此文件操作'),
        'input/output error': t('存储读写出错'), 'no such file or directory': t('找不到所需的文件'),
    })[m[1]]],
];
function errorMessage(text) {
    if (diagnosticText[text]) return diagnosticText[text];
    if (errorText[text]) return errorText[text];
    if (providerErrors[text]) return providerErrors[text];
    if (kernelErrors[text]) return kernelErrors[text];
    for (const [re, fn] of errorPatterns) {
        const m = re.exec(text);
        if (m) return fn(m);
    }
    const network = /^network (\w+) failed at (\w+)$/.exec(text);
    if (network) return diagnosticText[text] || t('{0}在 {1} 步骤失败', networkActions[network[1]] || t('网络处理'), network[2]);
    const wrapped = /^([^:]{1,80}): (.+)$/s.exec(text);
    if (wrapped && (errorText[wrapped[2]] || diagnosticText[wrapped[2]])) return errorMessage(wrapped[2]);
    return text;
}

export function diagnosticMessage(text) {
    if (!text) return '';
    if (diagnosticText[text]) return diagnosticText[text];
    // A task's ID is 24 hex digits (runJob), so an error that reads "x failed y" is not one.
    const task = /^(\S+) (done|failed|held) ([0-9a-f]{24})(?: (.*))?$/s.exec(text);
    if (task) {
        const extra = task[4] && task[4][0] !== '{' ? diagnosticMessage(task[4]) : '';
        return taskName(task[1]) + ' · ' + taskStage[task[2]] + (extra ? ' · ' + extra : '');
    }
    const dns = /^(dns_[a-z_]+) ([a-zA-Z0-9_-]{1,48})$/.exec(text);
    if (dns && diagnosticText[dns[1]]) return t('dnsmasq 实例 {0} · {1}', dns[2], diagnosticText[dns[1]]);
    const prefix = /^(starting|releasing|release_failed|protected|fail_open|running) (.+)$/s.exec(text);
    return prefix ? diagnosticText[prefix[1]] + ' · ' + diagnosticMessage(prefix[2]) : errorMessage(text);
}

// Log entries whose detail completes the sentence.
const detailSentences = {
    conflict_services_stopped: (d) => t('{0}：{1}', diagnosticText.conflict_services_stopped, d.split(', ').map(pluginName).join(t('、'))),
    // The core's own words, with addresses, host names, links, paths and names taken out
    // (records.go coreDetail).
    core_diagnostic: (d) => t('内核报告：{0}', d),
    // Which listener, or what in the configuration (redacted alike).
    core_listener_failed: (d) => t('{0}：{1}', diagnosticText.core_listener_failed, d),
    invalid_configuration: (d) => t('{0}：{1}', diagnosticText.invalid_configuration, d),
};
// An entry that counts lines of more different details than the log keeps apart within a minute
// (records.go LogDetail): the reason's own text, unless it speaks of a single line.
const foldedText = {
    core_diagnostic: t('内核报告了多条内容各不相同的其他消息，已合并为一条'),
    permission_denied: t('内核多次报告权限不足，被拒绝的操作各不相同，已合并为一条'),
};
export function eventMessage(v) {
    const sentence = v.detail && detailSentences[v.message];
    if (sentence) return sentence(v.detail);
    return (v.folded && foldedText[v.message]) || diagnosticMessage(v.message);
}

export function tlsMessage(detail) {
	const messages = {
		expired: t('HTTPS 证书已过期'), not_yet_valid: t('HTTPS 证书尚未生效'),
		hostname: t('HTTPS 证书与请求的域名不匹配'), untrusted: t('路由器不信任 HTTPS 证书的签发机构'),
		verification_failed: t('HTTPS 证书未通过校验'),
	};
	const message = messages[detail?.reason];
	if (!message || ![detail.checked_at, detail.not_before, detail.not_after].every(value => typeof value === 'string')) return message;
	const stamp = value => value.replace('T', ' ').replace(/(\.\d+)?Z$/, '');
	return t('{0}。核验时路由器时间 {1}，证书有效期 {2} 至 {3}（UTC）。请核对系统时间及服务端证书。', message, stamp(detail.checked_at), stamp(detail.not_before), stamp(detail.not_after));
}
