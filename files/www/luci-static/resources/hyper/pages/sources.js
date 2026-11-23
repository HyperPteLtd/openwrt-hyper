import { bytes, tlsMessage, datetime, day, say, hush, attach, diagnosticMessage, providerErrors } from '../dom.js';
// LuCI loads this module on demand. Subscription secrets are submitted directly
// to rpcd and never enter a route, storage key or display label.
const formats = {
    hyper: 'Hyper YAML',
    'sing-box': 'sing-box JSON',
    clash: 'Clash YAML',
    uri: '节点链接',
    surge: 'Surge',
    loon: 'Loon',
    quantumultx: 'Quantumult X',
    shadowrocket: 'Shadowrocket',
    stash: 'Stash',
    surfboard: 'Surfboard',
    xray: 'Xray / V2Ray JSON',
    sip008: 'SIP008',
    ssd: 'SSD',
};
const kinds = { platform: '服务商账户', file: '本地文件', external: '链接订阅' };
export const ids = () =>
    Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(16).padStart(2, '0')).join('');
export const actions = (u, ...children) => u.h('div', { class: 'ow-actions' }, ...children);
export const muted = (u, text) => u.h('span', { class: 'ow-muted' }, text);
export const badge = (u, text) => u.h('span', { class: 'ow-badge' }, text);
export const field = (u, title, control) => u.h('label', { class: 'ow-field' }, u.h('span', {}, title), control);
async function write(u, action, args) {
    try {
        return await u.run('subscription_write', { action, args });
    } catch (err) {
        // A refresh describes its own failure (importSource); the other actions' codes are read here.
        if (action === 'refresh' || err?.name === 'AbortError') throw err;
        const message = failureMessage(err.message, null, err.tls);
        throw message === err.message ? err : new Error(message);
    }
}
export const input = (u, attrs = {}) => u.h('input', attrs);
export const choice = (u, pairs, value, label) => {
    const select = u.h(
        'select',
        { 'aria-label': label },
        ...pairs.map(([v, name]) => u.h('option', { value: v }, name)),
    );
    select.value = value;
    return select;
};
export async function read(u, action, args = {}) {
    const result = await u.rpc('subscription_read', { action, args });
    if (result?.ok === false) throw new Error(result.error);
    return result?.data ?? result;
}
export const date = (s) => (s && Number.isFinite(Date.parse(s)) ? datetime(s).slice(0, 16) : '—');
export function form(u, controls, submitLabel, handler) {
    const body = u.h('form', { class: 'ow-stack' }, ...controls);
    const submit = u.button(
        submitLabel,
        (event) => {
            event.preventDefault();
            return body.reportValidity() && handler();
        },
        'ow-primary',
    );
    submit.type = 'submit';
    body.append(submit);
    body.addEventListener('submit', (event) => {
        event.preventDefault();
        if (!submit.disabled) submit.click();
    });
    return body;
}
export function confirm(u, title, content, label, handler) {
    const body = u.h('div', { class: 'ow-stack' }, content);
    const dialog = u.dialog(title, body);
    let accepted = false;
    const finish = new Promise((resolve) => dialog.addEventListener('close', () => resolve(accepted), { once: true }));
    body.append(
        actions(
            u,
            u.button('取消', () => dialog.close()),
            u.button(
                label,
                async () => {
                    accepted = (await handler()) !== false;
                    dialog.close();
                },
                'ow-danger',
            ),
        ),
    );
    return finish;
}
const notices = {
    mihomo_protocol_unsupported: '当前内核无法使用该协议，请切换到支持它的版本',
    mihomo_capability_unknown: '选择内核后可确认此节点是否可用',
    mihomo_router_field_ignored: '路由器使用自身的监听、安全及网络设置，已忽略模板中的对应选项',
    mihomo_unsupported_rule: '这条规则无法转换为内核规则',
    mihomo_unsupported_rule_set: '该规则集无法被内核直接使用，请提供 YAML、text 或 MRS 格式',
    subconv_unsupported_proxy: '该节点的协议无法转换，已跳过',
    subconv_unsupported_option: '该节点有无法转换的选项，已跳过',
    subconv_unsupported_rule: '这条规则无法转换为内核规则，已跳过',
    subconv_unsupported_rule_set: '该规则集无法转换为内核规则集，已跳过',
    subconv_unknown_reference: '引用了不存在的节点或策略组，已跳过',
    subconv_empty_group: '策略组没有可用成员，已跳过',
    subconv_invalid_proxy: '节点配置无效（地址、端口或参数有误），已跳过',
    subconv_ignored_option: '已移除路由器不使用或内核不接受的选项，其余保留',
    subconv_renamed: '名称为空、重复或为保留名，已改名',
    subconv_unsupported_provider: '该节点提供方无法使用，已跳过',
    subconv_local_provider: '该提供方读取本机文件，只能使用已导入路由器的规则文件',
    subconv_unsupported_group: '该策略组类型无法使用，已跳过',
    subconv_template_unavailable: '暂时取不到内置分流规则，已按默认分流：大陆直连，其余走代理。订阅会在下次更新时重试；本地文件不会自动更新，请稍后重新导入',
    subconv_cyclic_reference: '策略组或前置代理形成循环，已移除其中一处引用',
    node_missing_credential: '节点缺少认证信息，已跳过',
    script: '订阅依赖脚本分流，路由器无法执行',
    provider_download_failed: '订阅模板已取得，但其中引用的节点文件或规则集下载失败。请检查路由器到规则提供方的网络，稍后重试',
    superseded: '已有更新的请求接手了此订阅，请查看最新任务的结果',
    unavailable_inventory: '模板未导出全部已授权节点',
    local_fallback_removes_rejection: '切换到本地分流会移除原拦截规则',
    unsupported_node: '节点或协议选项无法转换',
    invalid_credential: '节点的认证信息无效',
    nonstandard_snell_credential: 'Snell 缺少标准 psk 认证字段，此节点无法导入',
    provider_required: '订阅引用了尚未载入的节点提供方',
    provider_option_ignored: '路由器无法执行节点提供方的这一选项，已忽略',
    provider_option_unsafe: '节点提供方的这一选项会改动路由器自身的网络设置，已拒绝',
    dialer_proxy_unavailable: '该节点经由的前置代理不存在或已跳过，此节点也已跳过',
    dialer_proxy_cycle: '该节点的前置代理链会绕回自身，已跳过',
    rule_provider_custom_geo: '订阅使用自定义地理数据库，当前无法还原该条地理规则',
    rule_provider_reference_unavailable: '规则引用的提供方未载入，相关规则无法导入',
    unsupported_group: '策略组无法转换',
    source_mismatch: '来源与当前订阅不一致',
    rejection_removed: '会移除原有的拦截规则',
    routing_disappeared: '新版本没有分流规则和策略组',
    active_target_missing: '正在使用的节点或策略组在新版本中不存在',
};
// Why a subscription routes by the built-in rules (the profile's template.reason).
const templateReasons = {
    requested: () => '已选择内置规则',
    no_rules: () => '订阅未附带分流规则',
    no_match: () => '订阅规则缺少最终规则',
    dropped_rules: (t) => t.dropped + ' / ' + t.total + ' 条订阅规则无法使用',
};
function templateLine(t) {
    const reason = templateReasons[t.reason]?.(t);
    return ['内置分流规则', reason, '规则版本 ' + day(t.version), t.cached ? '暂用本机副本' : '']
        .filter(Boolean)
        .join(' · ');
}
function moreRows(u, body, rows, render, noun) {
    let offset = 0;
    const append = () => {
        for (const row of rows.slice(offset, offset + 24)) body.append(render(row));
        offset += 24;
        if (offset < rows.length) {
            const more = u.button('显示更多' + noun + '（剩余 ' + (rows.length - offset) + ' 项）', () => {
                more.remove();
                append();
            });
            body.append(more);
        }
    };
    append();
}
const templateWarnings = {
    OTHER: '服务商还报告了当前插件无法识别的模板提醒',
    NO_GROUP_NODES: '策略组没有可用节点，服务商已将出口设为直连',
    UNSUPPORTED_SOURCE_RULES: '规则集中的部分规则无法用此模板表达，服务商已跳过',
    GLOBAL_TEST_URL: '模板使用统一的测速地址',
    UNSUPPORTED_RULE: '服务商已跳过此模板无法表达的规则',
    NODE_SUBSCRIPTION_REQUIRED: '此模板引用独立的节点订阅，请确认所引用的节点已导入',
    UNSUPPORTED_GROUP: '故障转移组已导出为手动选择组',
    REJECT_CANDIDATE: '模板无法让用户选择拦截出口，服务商已从可选成员中移除该项',
    STATIC_REJECT: '策略组已按默认设置固定为拦截',
    NODE_LIST_ONLY: '模板只下发节点，未附带服务商的分流规则',
    CUSTOM_TEMPLATE: '自定义模板未引用服务商生成的策略组和分流规则',
    NATIVE_LINK_UNAVAILABLE: '部分节点缺少生成分享链接所需的信息',
    UNSUPPORTED_NODE_OPTIONS: '部分节点的协议选项无法用此模板表达',
    NAIVE_CERTIFICATE_TRUST: 'Naive 节点需要路由器信任服务端证书',
};
function failureMessage(code, provider, tls) {
    const cert = tlsMessage(tls);
    if (!provider || provider.code !== code || !['node', 'rule'].includes(provider.kind)) return cert || notices[code] || providerErrors[code] || diagnosticMessage(code);
    const http = /^provider_http_([1-5][0-9]{2})$/.exec(code);
    const label = provider.kind === 'rule' ? '规则提供方' : '节点提供方';
    const issue = [label, provider.file, http ? '返回 HTTP ' + http[1] : cert || providerErrors[code] || '引用文件读取失败'].filter(Boolean).join(' · ');
    const rows = provider.template_warnings ? noticeRows([{code:'platform_routing_warnings', at:provider.template_warnings}]) : [];
    return rows.length ? issue + '。模板提醒：' + rows.map(row => row.text).join('；') : issue;
}
// An update that an automatic refresh downloaded but did not apply (the source's pending_update),
// and the button on its card that runs the card's update to review it.
const pendingUpdates = {
    confirmation_required: { text: '自动更新发现变更待确认，确认前节点和规则保持不变', action: '查看并确认' },
    update_blocked: { text: '自动更新取到的新版本无法使用，已保留上次配置', action: '查看原因' },
};
function pendingMessage(pending) {
    const details = (pending.notices || []).map((code) => notices[code] || diagnosticMessage(code)).filter(Boolean).slice(0, 2);
    return [pendingUpdates[pending.code]?.text || '自动更新取到的新版本尚未应用', ...details].join(' · ');
}

function noticeRows(items) {
    // The service sends null rather than [] when there is nothing to report.
    return (items || []).flatMap((n) => {
        if (n.code !== 'platform_routing_warnings') {
            const detail = missingRejectRule(n) ? '此拦截规则引用的规则集未载入' : notices[n.code] || diagnosticMessage(n.code);
            return [{ count: 1, text: [n.at, n.resource, detail].filter(Boolean).join(' · ') }];
        }
        if (/^[0-9]+$/.test(n.at)) {
            return [{ count: Number(n.at), text: '服务商报告了 ' + n.at + ' 条模板提醒，仅提供了总数。' }];
        }
        return (n.at || '').split(',').map(item => {
            const [code, raw] = item.split('=');
            const count = Number(raw) || 1;
            return { count, text: (templateWarnings[code] || '服务商模板提醒') + (count > 1 ? '（' + count + ' 条）' : '') };
        });
    });
}
function missingRejectRule(n) {
    return n.code === 'rule_provider_reference_unavailable' && n.dangerous === true;
}
function noticeSummary(items) {
    const warnings = items || [];
    const rows = noticeRows(warnings);
    if (!rows.length) return '';
    const count = rows.reduce((n, row) => n + row.count, 0);
    return count + ' 条导入提醒' + (rows.length === 1 && !/^[0-9]+$/.test(warnings[0].at) ? ' · ' + rows[0].text : '');
}
function review(u, result) {
    const changes = result?.changes || {};
    const lines = [
        ['新增节点', changes.added?.map((x) => x.name)],
        ['移除节点', changes.removed?.map((x) => x.name)],
        ['受影响的策略组', changes.affected_groups?.map((id) => u.name(id) || id)],
        ['引用已失效', changes.missing_active_targets?.map((id) => u.name(id) || id)],
    ];
    const rows = noticeRows(result?.notices);
    const body = u.h(
        'div',
        { class: 'ow-stack' },
        ...lines
            .filter((x) => x[1]?.length)
            .map(([key, values]) =>
                u.h('details', {}, u.h('summary', {}, key + ' · ' + values.length), u.h('p', {}, values.join('、'))),
            ),
    );
    moreRows(u, body, rows, row => u.h('p', {}, row.text), '提醒');
    return body;
}
async function retryDownload(u, source, group, content, feedback, change, needsFile) {
    const select = choice(u, group.options.map(option => [option.id, option.name]), group.selected, '规则下载出口');
    const controls = [field(u, group.name || '规则下载出口', select), muted(u, '此选择仅用于本订阅的规则下载。')];
    if (group.total > group.options.length) controls.push(muted(u, '显示 ' + group.options.length + ' / ' + group.total + ' 个成员。'));
    const file = needsFile && !content ? fileControl(u) : null;
    if (file) controls.push(file.node);
    let retry = false;
    const dialog = u.dialog('选择规则下载出口', form(u, controls, '保存并重试', async () => {
        if (file) content = await file.text();
        await write(u, 'preferences', { source, preferences: { download_choice: { group: group.id, member: select.value } } });
        retry = true;
        dialog.close();
    }));
    await new Promise(resolve => dialog.addEventListener('close', resolve, { once: true }));
    return retry && !u.signal.aborted && importSource(u, source, content, feedback, undefined, change);
}
// With a feedback element the outcome is written there, beside the control that started it: a
// failure names it as shown so the page does not repeat it while it is on screen, and a blocked
// version opens its review in place. Without one, or while 全部更新 queues reviews (present), the
// review opens over the page. A change ({replacement_url, rules}) is a link or routing choice the
// update is read with: the router saves it with the update it applies, and only then.
export async function importSource(u, source, content, feedback, confirmation, change, present) {
    const previous = (u.sources || []).some((s) => s.source === source && s.ready);
    if (feedback) {
        feedback.hidden = false;
        feedback.textContent = content ? '正在导入…' : '正在更新…';
        delete feedback.dataset.tone;
    }
    try {
        const r = await write(u, 'refresh', {
            source,
            ...(content ? { content } : {}),
            ...(confirmation ? { confirm: confirmation } : {}),
            ...change,
        });
        if (u.signal.aborted) return false;
        if (r?.decision === 'confirmation_required') {
            if (feedback) feedback.textContent = '等待确认更改';
            const ask = () => confirm(
                u,
                '确认订阅变更',
                u.h('div', {},
                    u.h('p', {}, r.retains_routing ? '以下规则无法用于本次配置。确认后将跳过这些条目，保留其余分流规则和策略组。' : '部分配置无法按原样导入。应用前请核对以下变化。'),
                    r.notices?.some(missingRejectRule)
                        ? u.h('p', {class:'ow-warning'}, '此次变更会跳过拦截规则。原本由它拦截的流量会继续匹配其余规则和兜底出口。')
                        : null,
                    review(u, r),
                ),
                r.retains_routing ? '保留其余分流并应用' : '应用此版本',
                () => importSource(u, source, content, feedback, r.confirmation_id, change),
            );
            const ok = await (present ? present(ask) : ask());
            if (!ok && feedback) feedback.textContent = previous ? '已保留现有配置' : '已取消本次导入';
            return ok;
        }
        if (r?.decision !== 'ready') {
            const message = previous ? '已保留上次配置' : '此配置无法导入';
            if (feedback && !present) {
                feedback.replaceChildren(u.h('details', {}, u.h('summary', {}, message), review(u, r)));
                feedback.dataset.tone = 'error';
                return false;
            }
            if (feedback) feedback.textContent = message;
            const open = () => u.dialog(message, review(u, r));
            if (present) await present(() => new Promise(resolve => open().addEventListener('close', resolve, { once: true })));
            else open();
            return false;
        }
        if (feedback)
            feedback.replaceChildren(
                u.h('details', {}, u.h('summary', {}, ['更新完成', r.node_count + ' 个节点', noticeSummary(r.notices)].filter(Boolean).join(' · ')), review(u, r)),
            );
        return true;
    } catch (err) {
        const description = failureMessage(err.message, err.provider, err.tls);
        if (feedback) {
            feedback.textContent = '更新失败 · ' + description + (previous ? ' · 已保留上次配置' : '');
            feedback.dataset.tone = 'error';
        }
        const group = err.provider?.download_group;
        if (group?.options?.length && !u.signal.aborted) {
            const retry = () => retryDownload(u, source, group, content, feedback, change, false);
            if (!feedback) return present ? present(retry) : retry();
            feedback.append(' ', u.button('选择规则下载出口', async () => { if (await retry()) await u.refresh(); }));
            return false;
        }
        const failure = description !== err.message ? new Error(description) : err;
        if (feedback) failure.shown = feedback;
        throw failure;
    }
}
async function chooseSource(u, s) {
    if (!s.ready && !(await importSource(u, s.source))) return;
    await u.run('source', { source: s.source });
    await u.refresh();
}
function agentField(u, agents, existing) {
    // Only the presets; a custom UA saved earlier stays selectable on that source.
    const choices = [...agents.map((x) => [x, x]), ...(existing?.custom_agent ? [['custom', '自定义 UA']] : [])];
    const selected = existing?.custom_agent ? 'custom' : existing?.agent || agents[0];
    const sel = choice(u, choices, selected, '订阅请求 UA');
    const custom = input(u, {
        type: 'text',
        maxlength: 256,
        placeholder: existing?.custom_agent ? '留空保留当前自定义 UA' : 'User-Agent',
        autocomplete: 'off',
        'aria-label': '自定义 UA',
    });
    const update = () => {
        custom.hidden = sel.value !== 'custom';
        custom.disabled = custom.hidden;
        custom.required = !custom.hidden && !existing?.custom_agent;
    };
    sel.addEventListener('change', update);
    update();
    return {
        node: u.h('div', { class: 'ow-stack' }, field(u, '订阅请求 UA', sel), custom),
        values: () => ({
            user_agent: sel.value === 'custom' ? custom.value.trim() : sel.value,
            keep_user_agent: !!existing?.custom_agent && sel.value === 'custom' && !custom.value.trim(),
        }),
    };
}
function fileControl(u) {
    const f = input(u, {
        type: 'file',
        required: true,
        accept: '.json,.yml,.yaml,.txt,.conf,.ini,.list,.sub,text/plain',
        'aria-label': '选择配置文件',
    });
    return {
        input: f,
        node: field(u, '配置文件', f),
        text: async () => {
            const item = f.files[0];
            // rpcd/ubus can have a smaller transport limit than the importer.
            if (!item || item.size > 8 * 1024 * 1024) throw Error('请选择 8 MiB 以内的配置文件');
            return item.text();
        },
    };
}
export function reimport(u, source, feedback) {
    const f = fileControl(u);
    const dialog = u.dialog(
        '导入新文件',
        form(u, [f.node], '导入', async () => {
            const ok = await importSource(u, source, await f.text(), feedback);
            dialog.close();
            if (ok) await u.refresh();
        }),
    );
}
function login(u, mount, close) {
    const origin = input(u, { type: 'url', required: true, placeholder: 'https://', autocomplete: 'url' });
    mount.replaceChildren(
        form(u, [field(u, '服务商地址', origin)], '验证服务商', async () => {
            const url = new URL(origin.value.trim());
            if (
                url.protocol !== 'https:' ||
                url.pathname !== '/' ||
                url.search ||
                url.hash ||
                url.username ||
                url.password
            )
                throw Error('请填写服务商 HTTPS 站点地址');
            const site = await write(u, 'site', { origin: url.origin });
            const email = input(u, { type: 'email', required: true, autocomplete: 'username' });
            const password = input(u, { type: 'password', required: true, autocomplete: 'current-password' });
            mount.replaceChildren(
                form(
                    u,
                    [
                        u.h('strong', {}, site.name),
                        muted(u, url.host),
                        field(u, '邮箱', email),
                        field(u, '密码', password),
                    ],
                    '登录',
                    async () => {
                        const data = await write(u, 'login', {
                            origin: url.origin,
                            email: email.value.trim(),
                            password: password.value,
                        });
                        password.value = '';
                        const plans = data.plans || [];
                        const usable = plans.filter(p => p.usable);
                        const automatic = !u.status.saved?.source && usable.length === 1 ? usable[0] : null;
                        const feedback = u.h('div', { role: 'status', class: 'ow-muted', hidden: true });
                        const finish = async () => { close(); await u.refresh(); };
                        async function load(i, choose) {
                            if (await importSource(u, data.sources[i], '', feedback)) {
                                if (choose) await u.run('source', {source: data.sources[i]});
                                await finish();
                            }
                        }
                        let retry;
                        const rows = plans.map((p, i) => {
                            const auto = p === automatic;
                            const label = !p.usable ? '暂不可用' : auto ? '重新下载' : '使用';
                            const button = u.button(label, () => load(i, !auto));
                            button.disabled = !p.usable;
                            if (auto) {
                                retry = button;
                                retry.disabled = true;
                            }
                            return u.h(
                                'div',
                                { class: 'ow-row' },
                                u.h(
                                    'div',
                                    {},
                                    u.h('strong', {}, p.planName || '套餐'),
                                    u.h('div', { class: 'ow-muted' }, p.expireAt ? '到期 ' + date(p.expireAt) : '长期有效'),
                                ),
                                button,
                            );
                        });
                        mount.replaceChildren(
                            u.h('h3', {}, automatic ? '导入套餐' : '选择套餐'),
                            ...rows, feedback,
                            ...(rows.length ? [] : [muted(u, '账户暂无套餐')]),
                            u.button('完成', finish),
                        );
                        if (automatic) {
                            try { await load(plans.indexOf(automatic), false); }
                            finally { retry.disabled = false; }
                        }
                    },
                ),
            );
        }),
    );
}
// Save the subscription first so its card shows at once, then import in the background;
// the card reads "正在导入…" until the download and conversion finish. A failure is told on the
// new card (or the overview's subscription card), unless the card already shows the failure the
// router recorded; on any other page it goes to the alert at the top.
async function saveNewSource(u, config, content, close) {
    await write(u, 'configure', { config });
    const importing = (u.memory.importing ||= new Set());
    importing.add(config.id);
    if (close) close();
    await u.refresh();
    // u.refresh() aborts the page's signal; the import must outlive the page it started on.
    const background = { ...u, signal: new AbortController().signal };
    let failure = '';
    importSource(background, config.id, content)
        .catch((e) => { failure = e.message; })
        .finally(async () => {
            importing.delete(config.id);
            await u.refresh();
            if (failure) u.tell(['source:' + config.id, 'sources'], [config.name, '导入失败', failure].filter(Boolean).join(' · '));
        });
    return true;
}
export function onboarding(u, environment, trigger) {
    const body = u.h('div', { class: 'ow-onboarding' });
    const dialog = u.dialog('启用全屋代理', body);
    const close = () => dialog.close();
    dialog.addEventListener('close', () => {
        if (trigger.isConnected && !document.querySelector('dialog[open]')) trigger.focus({ preventScroll: true });
    }, { once: true });
    const loginPane = u.h('div', { class: 'ow-stack' });
    login(u, loginPane, close);
    const link = input(u, { type: 'url', required: true, placeholder: 'https://…', autocomplete: 'off' });
    const f = fileControl(u);
    const linkID = 'src-' + ids(),
        fileID = 'src-' + ids();
    const picker = u.button('或导入配置文件', () => f.input.click(), 'ow-link');
    f.input.hidden = true;
    const progress = u.h('span', { role: 'status', class: 'ow-muted' });
    // A file that cannot be read or saved is reported under the picker.
    const fileLine = u.feedback();
    f.input.addEventListener('change', async () => {
        if (!f.input.files?.[0]) return;
        const name = f.input.files[0].name || '本地配置';
        picker.disabled = true;
        progress.textContent = '正在导入…';
        hush(fileLine);
        try {
            await saveNewSource(u, { id: fileID, kind: 'file', name: name.slice(0, 128) }, await f.text(), close);
        } catch (e) {
            if (e.name !== 'AbortError') say(fileLine, diagnosticMessage(e.message), { control: picker });
        } finally {
            picker.disabled = false;
            progress.textContent = '';
            f.input.value = '';
        }
    });
    const linkForm = form(u, [field(u, '粘贴订阅链接', link)], '添加订阅', async () => {
        const address = new URL(link.value.trim());
        await saveNewSource(u, { id: linkID, kind: 'external', name: address.hostname, url: address.href }, '', close);
    });
    linkForm.querySelector('button[type=submit]').className = 'ow-secondary-submit';
    body.append(
        muted(u, '登录 Hyper 服务商，或直接添加订阅链接、导入配置文件。'),
        u.h('section', { class: 'ow-setup-section' }, u.h('h3', {}, '登录 Hyper 服务商'), loginPane),
        u.h('section', { class: 'ow-setup-section' }, linkForm, actions(u, picker, f.input, progress), fileLine),
        u.h('h3', {}, '运行环境检查'),
        environment,
    );
}
async function addSource(u, agents) {
    const host = u.h('div', { class: 'ow-stack' }),
        pane = u.h('div', { class: 'ow-stack' });
    const dialog = u.dialog('添加订阅', host);
    const tabs = u.h('div', { class: 'ow-segment', role: 'group', 'aria-label': '订阅类型' });
    host.append(tabs, pane);
    function select(kind) {
        [...tabs.children].forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.kind === kind)));
        if (kind === 'platform') {
            login(u, pane, () => dialog.close());
            return;
        }
        const filename = input(u, {
            type: 'text', maxlength: 128,
            placeholder: kind === 'file' ? '留空使用文件名' : '留空使用订阅名称或域名',
        });
        const link = input(u, { type: 'url', required: true, placeholder: 'https://', autocomplete: 'off' });
        const f = fileControl(u),
            a = agentField(u, agents);
        const configured = { id: '' };
        pane.replaceChildren(
            form(
                u,
                [field(u, '名称', filename), ...(kind === 'file' ? [f.node] : [field(u, '订阅链接', link), a.node])],
                '添加并导入',
                async () => {
                    const content = kind === 'file' ? await f.text() : '';
                    if (!configured.id) configured.id = 'src-' + ids();
                    // Unnamed links are named by the router; unnamed files after the file.
                    let name = filename.value.trim();
                    if (!name && kind === 'file') name = (f.input.files[0]?.name || '').replace(/\.[^.]*$/, '') || '本地文件';
                    const config = { id: configured.id, name, kind };
                    if (kind === 'external') {
                        config.url = link.value.trim();
                        config.user_agent = a.values().user_agent;
                    }
                    await saveNewSource(u, config, content, () => dialog.close());
                },
            ),
        );
    }
    for (const [key, title] of [
        ['external', '订阅链接'],
        ['platform', '服务商登录'],
        ['file', '本地文件'],
    ]) {
        const b = u.button(title, () => select(key));
        b.dataset.kind = key;
        tabs.append(b);
    }
    select('external');
}
async function editSource(u, s, agents) {
    const a = s.kind === 'external' ? agentField(u, agents, s) : null;
    const name = input(u, { type: 'text', value: s.name || '', required: true, maxlength: 128 });
    const previousURL = s.kind === 'external' ? (await read(u, 'url', { source: s.source })).url || '' : '';
    const url = input(u, { type: 'url', value: previousURL, required: true, placeholder: 'https://', autocomplete: 'off' });
    const fallback = input(u, { type: 'checkbox' });
    fallback.checked = !!s.local_fallback;
    // The routing choice in use (s.rules) changes only with an update read with the new one.
    const routing = choice(u, [['', '订阅自带'], ['template', '内置规则']], s.rules || '', '分流规则');
    const switched = () => routing.value !== (s.rules || '');
    // A file is not downloaded again: a new routing choice comes with the file imported again.
    const file = s.kind === 'file' ? fileControl(u) : null;
    if (file) {
        const toggle = () => {
            file.node.hidden = !switched();
            file.input.disabled = !switched();
        };
        routing.addEventListener('change', toggle);
        toggle();
    }
    const controls = [];
    if (s.kind !== 'platform') controls.push(field(u, '名称', name));
    if (s.kind === 'external') controls.push(field(u, '订阅链接', url));
    if (a) controls.push(a.node);
    controls.push(
        field(u, '分流规则', routing),
        muted(u, s.kind === 'file'
            ? '订阅自带：订阅没有可用的规则时改用内置规则。更改需重新导入配置文件，导入后生效。'
            : '订阅自带：订阅没有可用的规则时改用内置规则。'),
        ...(file ? [file.node] : []),
        field(u, '可转换时允许使用本地分流', fallback),
    );
    const body = u.h('div', { class: 'ow-stack' });
    const dialog = u.dialog('编辑订阅', body);
    body.append(
        form(u, controls, '保存', async () => {
            const content = file && switched() ? await file.text() : '';
            const preferences = {
                ...(a ? a.values() : {}),
                ...(s.kind === 'platform' ? {} : { name: name.value.trim() }),
                allow_local_fallback: fallback.checked,
            };
            await write(u, 'preferences', { source: s.source, preferences });
            const nextURL = url.value.trim();
            // A changed link is verified by downloading it before it replaces the old one, and a
            // changed routing choice by the update read with it: the router saves either only
            // with the update it applies.
            const change = {
                ...(s.kind === 'external' && nextURL !== previousURL ? { replacement_url: nextURL } : {}),
                ...(switched() ? { rules: routing.value } : {}),
            };
            if (Object.keys(change).length && !(await importSource(u, s.source, content, undefined, undefined, change))) {
                // Cancelled or not applied: the editor shows the routing still in use.
                routing.value = s.rules || '';
                routing.dispatchEvent(new Event('change'));
                return;
            }
            dialog.close();
            await u.refresh();
        }),
    );
    const other = [];
    if (s.download_override) other.push(u.button('恢复订阅指定的下载出口', async () => {
        await write(u, 'preferences', { source: s.source, preferences: { download_choice: { group: '', member: '' } } });
        dialog.close();
        await u.refresh();
    }));
    if (s.has_history)
        other.push(
            u.button('恢复上一份快照', () =>
                confirm(u, '恢复上一份快照', muted(u, s.name), '恢复', async () => {
                    await write(u, 'rollback', { source: s.source });
                    dialog.close();
                    await u.refresh();
                }),
            ),
        );
    if (other.length) body.append(actions(u, ...other));
}
// Signing out of a provider or removing a subscription sits on the card, not in its editor.
function removeButton(u, s) {
    const title = s.kind === 'platform' ? '退出服务商账户' : '移除订阅';
    return u.button(
        s.kind === 'platform' ? '退出账户' : '移除',
        () =>
            confirm(
                u,
                title,
                u.h(
                    'p',
                    {},
                    s.kind === 'platform'
                        ? '此账户下所有套餐的凭据和快照都将移除。'
                        : '此订阅的节点、规则和历史快照都将移除。',
                ),
                title,
                async () => {
                    await write(u, s.kind === 'platform' ? 'logout' : 'revoke', { source: s.source });
                    await u.refresh();
                },
            ),
        'ow-link ow-danger',
    );
}
export async function subscriptions(u) {
    const [agents, prefs] = await Promise.all([read(u, 'agents'), u.rpc('network_preferences').catch(() => ({}))]),
        sources = (u.sources || []).filter((s) => !s.revoked);
    const current = u.status.intent?.source;
    const feedback = new Map(),
        grid = u.h('div', { class: 'ow-source-grid ow-grid' });
    const canRefresh = (s) => s.kind !== 'file' && !s.login_required;
    const updateAll = u.button('全部更新', async () => {
        const errors = [];
        const queue = sources.filter(canRefresh)[Symbol.iterator]();
        // At most two transfers at once on the router. Review one plan at a time.
        let reviewDone = Promise.resolve();
        const present = show => {
            const next = reviewDone.then(() => !u.signal.aborted && show());
            reviewDone = next.catch(() => {});
            return next;
        };
        const worker = async () => {
            for (const s of queue) {
                if (u.signal.aborted) return;
                try {
                    await importSource(u, s.source, '', feedback.get(s.source), undefined, undefined, present);
                } catch {
                    errors.push(s.name);
                }
            }
        };
        await Promise.all([worker(), worker()]);
        if (!u.signal.aborted) {
            await u.refresh();
            // The cards say why; the line under 全部更新 names which ones failed.
            if (errors.length) u.tell('update-all', '更新失败 · ' + errors.join('、'));
        }
    });
    updateAll.disabled = !sources.some(canRefresh);
    function sourceCard(s) {
        const warning = noticeSummary(s.notices);
        const pending = !s.last_failure && canRefresh(s) ? s.pending_update : null;
        const message = u.h(
            'div',
            {
                class: 'ow-muted', role: 'status', hidden: !s.last_failure && !pending && !s.last_change && !s.needs_refresh,
                'data-tone': s.last_failure ? 'error' : null,
            },
            s.last_failure
                ? '上次更新失败 · ' + failureMessage(s.last_failure, s.last_provider, s.last_tls) + (s.ready ? ' · 已保留上次配置' : '')
                : pending ? pendingMessage(pending)
                : s.needs_refresh ? (s.kind === 'file' ? '内核已切换，请重新导入原文件以生成配置' : '内核已切换，等待重新获取订阅')
                : s.last_change
                  ? '上次更新 · 新增 ' + s.last_change.added + ' · 移除 ' + s.last_change.removed
                  : '',
        );
        if (s.last_provider?.download_group?.options?.length && !s.suspended && !s.login_required) {
            message.append(' ', u.button('选择规则下载出口', async () => {
                if (await retryDownload(u, s.source, s.last_provider.download_group, undefined, message, undefined, s.kind === 'file')) await u.refresh();
            }));
        }
        const importing = !!u.memory.importing?.has(s.source);
        if (importing) {
            message.hidden = false;
            message.textContent = s.kind === 'file' ? '正在导入…' : '正在获取订阅…';
        }
        feedback.set(s.source, message);
        const isCurrent = current === s.source;
        const use = isCurrent ? badge(u, '正在使用') : u.button('设为当前', () => chooseSource(u, s));
        if (!isCurrent) use.disabled = !!s.suspended || !!s.login_required || (!s.ready && s.kind === 'file');
        use.classList.add('ow-source-use');
        const avatar = u.h('span', { class: 'ow-avatar', 'aria-hidden': 'true' }, Array.from(s.name || '订阅')[0]);
        // The kind is named in words as well as colour: an account card is tinted, the others stay neutral.
        const kind = kinds[s.kind] ? u.h('span', { class: 'ow-source-kind' }, kinds[s.kind]) : null;
        const agentLabel = s.kind === 'external' && (s.custom_agent ? '自定义 UA' : s.agent);
        const interval = s.kind !== 'file' && Number.isInteger(prefs.update_hours)
            ? (prefs.update_hours > 0 ? '每 ' + prefs.update_hours + ' 小时自动更新' : '手动更新')
            : '';
        const meta = [formats[s.format] || s.format, agentLabel, interval]
            .filter(Boolean)
            .join(' · ');
        const plan = s.usage || s.plan,
            used = plan ? (Number(plan.upload) || 0) + (Number(plan.download) || 0) : 0;
        const planInfo = plan
            ? u.h(
                  'div',
                  { class: 'ow-stack' },
                  ...(plan.total > 0
                      ? [
                            u.h('progress', {
                                value: Math.min(used, plan.total),
                                max: plan.total,
                                'aria-label': '已用套餐流量',
                            }),
                        ]
                      : []),
                  u.h(
                      'div',
                      { class: 'ow-row' },
                      muted(u, bytes(used) + (plan.total != null ? ' / ' + bytes(plan.total) : '')),
                      muted(
                          u,
                          plan.expireAt
                              ? '到期 ' + day(plan.expireAt)
                              : !s.usage || s.usage.no_expiry
                                ? '长期有效'
                                : '',
                      ),
                  ),
              )
            : null;
        const metadata = u.h(
            'div',
            { class: 'ow-row' },
            u.h('span', {}, u.h('strong', { class: 'ow-mono' }, String(s.node_count || 0)), ' 个节点'),
            muted(u, s.updated_at ? '上次更新 ' + date(s.updated_at) : '尚未更新'),
        );
        const refresh = () => importSource(u, s.source, '', message).then(async ok => {
            if (!ok || u.signal.aborted) return;
            const updated = await read(u, 'status', {source: s.source});
            if (u.signal.aborted) return;
            if (updated.revoked || !updated.source) return u.refresh();
            const index = sources.indexOf(s);
            if (index < 0) return;
            sources[index] = updated;
            const original = u.sources.indexOf(s);
            if (original >= 0) u.sources[original] = updated;
            const replacement = sourceCard(updated);
            // Focus in the card moves to the same button on the new one, or to its 更新 where that
            // button is gone, as a pending update's is once the update applied.
            const focused = card.contains(document.activeElement) && (document.activeElement.dataset.action || 'update');
            card.replaceWith(replacement);
            if (focused)
                (replacement.querySelector('[data-action="' + focused + '"]') || replacement.querySelector('[data-action="update"]'))
                    ?.focus({ preventScroll: true });
            updateAll.disabled = !sources.some(canRefresh);
        });
        // The card's update. A pending update's button runs it too, to open the update's review: the
        // two share one run, and both read busy until it ends, so a second review cannot stack on it.
        let running = null;
        const runUpdate = () => {
            if (s.kind === 'file') return reimport(u, s.source, message);
            if (s.login_required) {
                const pane = u.h('div', {}),
                    d = u.dialog('登录服务商', pane);
                return login(u, pane, () => d.close());
            }
            if (!running) {
                const busy = (on) => {
                    for (const b of [update, pendingButton].filter(Boolean))
                        for (const name of ['aria-busy', 'aria-disabled'])
                            if (on) b.setAttribute(name, 'true');
                            else b.removeAttribute(name);
                };
                busy(true);
                running = refresh().finally(() => {
                    running = null;
                    busy(false);
                });
            }
            return running;
        };
        const update = u.button(s.kind === 'file' ? '导入新文件' : s.login_required ? '重新登录' : '更新', runUpdate);
        update.disabled = importing;
        update.dataset.action = 'update';
        // It sits with the card's buttons: the line above reports the update's progress in its place.
        const pendingButton = pending && !importing ? u.button(pendingUpdates[pending.code]?.action || '查看', runUpdate) : null;
        if (pendingButton) pendingButton.dataset.action = 'pending';
        const edit = u.button('编辑', () => editSource(u, s, agents));
        edit.dataset.action = 'edit';
        const remove = removeButton(u, s);
        remove.dataset.action = 'remove';
        // The card's other actions report under its buttons; a failed background import is told
        // here too, unless the message above already shows the failure the router recorded.
        const cardLine = u.feedback(null, 'source:' + s.source, { covered: !!s.last_failure });
        const heading = 'ow-source-' + ids();
        const card = u.h(
            'section',
            { class: 'ow-card ow-source-card', 'data-kind': s.kind },
            u.h(
                'div',
                { class: 'ow-source-title' },
                avatar,
                kind,
                u.h('strong', { class: 'ow-source-name', id: heading, title: s.name || '订阅' }, s.name || '订阅'),
                u.h('div', { class: 'ow-muted ow-source-meta', hidden: !meta }, meta),
                use,
            ),
            metadata,
            ...(s.template ? [muted(u, templateLine(s.template))] : []),
            ...(planInfo ? [planInfo] : []),
            ...(s.suspended ? [badge(u, '套餐暂不可用')] : []),
            ...(warning ? [u.h('details', {}, u.h('summary', {}, warning), review(u, s))] : []),
            message,
            actions(u, pendingButton, update, edit, remove),
            cardLine,
        );
        // Every card has the same buttons; the subscription's name tells them apart.
        for (const b of card.querySelectorAll('button')) b.setAttribute('aria-describedby', heading);
        // A pending update's button is described by the line that says what waits.
        if (pendingButton) {
            message.id = heading + '-status';
            pendingButton.setAttribute('aria-describedby', heading + ' ' + message.id);
        }
        attach(card, cardLine);
        return card;
    }
    grid.append(...sources.map(sourceCard));
    const add = u.button('＋ 添加订阅', () => addSource(u, agents), 'ow-primary');
    const toolbar = u.h('div', { class: 'ow-row ow-toolbar' }, u.h('h2', {}, '订阅'), actions(u, updateAll, add));
    const body = u.h(
        'div',
        { class: 'ow-stack' },
        toolbar,
        u.feedback(toolbar, 'update-all'),
        sources.length
            ? grid
            : u.h(
                  'div',
                  { class: 'ow-card ow-empty' },
                  u.h('h3', {}, '尚未添加订阅'),
                  muted(u, '登录服务商、粘贴订阅链接或从文件导入节点。'),
              ),
    );
    if (u.detail === 'add') u.afterRender = () => {
        history.replaceState(history.state, '', '#subscriptions');
        addSource(u, agents);
    };
    return body;
}
