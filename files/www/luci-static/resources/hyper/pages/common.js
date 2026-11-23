// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.
//
// What the Hyper pages share: small controls, the Hyper account API through the router, the
// account's subscription sync, and how plans, amounts and dates read.
import { datetime, diagnosticMessage, tlsMessage } from '../dom.js';

export const HYPER = 'hyper';

export const ids = () =>
    Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(16).padStart(2, '0')).join('');
export const actions = (u, ...children) => u.h('div', { class: 'ow-actions' }, ...children);
export const muted = (u, text) => u.h('span', { class: 'ow-muted' }, text);
export const badge = (u, text) => u.h('span', { class: 'ow-badge' }, text);
export const field = (u, title, control) => u.h('label', { class: 'ow-field' }, u.h('span', {}, title), control);
export const input = (u, attrs = {}) => u.h('input', attrs);
export const choice = (u, pairs, value, label) => {
    const select = u.h('select', { 'aria-label': label }, ...pairs.map(([v, name]) => u.h('option', { value: v }, name)));
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
export function confirm(u, title, content, label, handler, tone = 'ow-danger') {
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
                tone,
            ),
        ),
    );
    return finish;
}

// --- The Hyper account ------------------------------------------------------------------------

// write runs a subscription helper method as a router task and returns its data.
export const write = (u, action, args = {}) => u.run('subscription_write', { action, args });

// The account's sign-in state: { signed_in, email }.
export const account = (u) => read(u, 'account');

// api calls the Hyper service through the router (allowlisted paths; the router adds the
// session, which the page never sees). It returns { ok, status, code, msg, data, error }.
export function api(u, method, path, { query = {}, body, idempotencyKey } = {}) {
    return write(u, 'api', {
        api_method: method,
        path,
        api_query: { language: 'zh-CN', ...query },
        ...(body !== undefined ? { body } : {}),
        ...(idempotencyKey ? { idempotency_key: idempotencyKey } : {}),
    });
}
// The account API's own codes (internal/account), which reach the page in a reply's error.
const accountErrors = {
    not_signed_in: '尚未登录 Hyper 账户',
    api_unavailable: '暂时无法连接 Hyper 服务，请检查网络后重试',
    timeout: '请求超时，请稍后重试',
    invalid_json: 'Hyper 服务返回的内容无法读取，请稍后重试',
    login_failed: '登录失败，请检查验证码',
    register_failed: '注册失败',
    session_write_failed: '无法保存登录状态，请检查路由器存储空间',
    no_subscription: '账户还没有可用的套餐',
    subscribe_list_failed: '无法读取账户套餐',
    session_changed: '登录账户已变更，请刷新页面',
};
export const apiError = (r, fallback = '请求失败') =>
    r?.msg || accountErrors[r?.error] || (r?.error ? diagnosticMessage(r.error) : '') || fallback;

// --- Syncing the account's nodes and rules -----------------------------------------------------

const notices = {
    mihomo_protocol_unsupported: '当前内核无法使用该协议，请切换到支持它的版本',
    mihomo_capability_unknown: '选择内核后可确认此节点是否可用',
    mihomo_unsupported_rule: '这条规则无法转换为内核规则',
    mihomo_unsupported_rule_set: '该规则集无法被内核直接使用',
    mihomo_router_field_ignored: '路由器使用自身的监听、安全及网络设置，已忽略对应选项',
    node_missing_credential: '节点缺少认证信息，已跳过',
    unsupported_node: '节点或协议选项无法使用，已跳过',
    invalid_credential: '节点的认证信息无效，已跳过',
    nonstandard_snell_credential: 'Snell 节点缺少标准 psk 认证字段，已跳过',
    provider_required: '引用了尚未载入的节点提供方',
    dialer_proxy_unavailable: '该节点经由的前置代理不存在或已跳过，此节点也已跳过',
    dialer_proxy_cycle: '该节点的前置代理链会绕回自身，已跳过',
    rule_provider_custom_geo: '使用了自定义地理数据库，这条地理规则无法还原',
    rule_provider_reference_unavailable: '规则引用的规则集未载入，相关规则无法使用',
    unsupported_group: '策略组无法使用，已跳过',
    source_mismatch: '来源与当前订阅不一致',
    rejection_removed: '会移除原有的拦截规则',
    routing_disappeared: '新版本没有分流规则和策略组',
    active_target_missing: '正在使用的节点或策略组在新版本中不存在',
    local_fallback_removes_rejection: '切换到本地分流会移除原拦截规则',
    superseded: '已有更新的请求接手了这次同步，请查看最新任务的结果',
    // Share links the router reads (internal/links): each only leaves its node out.
    link_unsupported_proxy: '该节点的协议暂不支持，已跳过',
    link_unsupported_option: '该节点有无法使用的选项，已跳过',
    link_invalid_proxy: '该节点的链接不完整，已跳过',
    link_ignored_option: '该节点的部分选项由路由器决定，已忽略',
    link_unsupported_rule: '这条规则无法使用，已跳过',
    link_unsupported_rule_set: '这个规则集无法使用，已跳过',
    link_unsupported_provider: '这个节点来源无法使用，已跳过',
    link_local_provider: '这个节点来源指向本地文件，已跳过',
    link_unsupported_group: '这个策略组无法使用，已跳过',
    link_unknown_reference: '引用了不存在的节点或策略组，已跳过',
    link_cyclic_reference: '策略组之间互相引用，已跳过',
    link_empty_group: '策略组没有可用节点，已跳过',
    link_renamed: '有重名的节点，已自动改名',
    link_template_unavailable: '默认分流规则暂时不可用',
    link_confirmation_required: '这部分内容需要确认后才能使用',
};
// An update a scheduled sync read but did not apply (the source's pending_update), and the
// button on the sync card that reads it again to review it.
export const pendingUpdates = {
    confirmation_required: { text: '自动同步发现变更待确认，确认前节点和规则保持不变', action: '查看并确认' },
    update_blocked: { text: '自动同步取到的新版本无法使用，已保留上次的节点', action: '查看原因' },
};
const noticeText = (n) => [n.at, n.resource, notices[n.code] || diagnosticMessage(n.code)].filter(Boolean).join(' · ');

export function syncFailure(err) {
    return tlsMessage(err?.tls) || diagnosticMessage(err?.message ?? String(err));
}

// review lists what a sync would change: nodes added and removed, groups affected, notices.
export function review(u, result) {
    const changes = result?.changes || {};
    const lines = [
        ['新增节点', changes.added?.map((x) => x.name)],
        ['移除节点', changes.removed?.map((x) => x.name)],
        ['受影响的策略组', changes.affected_groups?.map((id) => u.name(id) || id)],
        ['引用已失效', changes.missing_active_targets?.map((id) => u.name(id) || id)],
    ];
    return u.h(
        'div',
        { class: 'ow-stack' },
        ...lines
            .filter((x) => x[1]?.length)
            .map(([key, values]) => u.h('details', {}, u.h('summary', {}, key + ' · ' + values.length), u.h('p', {}, values.join('、')))),
        ...(result?.notices || []).slice(0, 50).map((n) => u.h('p', { class: 'ow-muted' }, noticeText(n))),
    );
}

// syncHyper reads the account's nodes and rules now. The outcome goes to feedback when given;
// an update that needs confirmation is shown for review first. True once it applied.
export async function syncHyper(u, feedback, confirmation) {
    const previous = (u.sources || []).some((s) => s.source === HYPER && s.ready);
    if (feedback) {
        feedback.hidden = false;
        feedback.textContent = '正在同步节点…';
        delete feedback.dataset.tone;
    }
    try {
        const r = await write(u, 'refresh', { source: HYPER, ...(confirmation ? { confirm: confirmation } : {}) });
        if (u.signal.aborted) return false;
        if (r?.decision === 'confirmation_required') {
            if (feedback) feedback.textContent = '等待确认更改';
            const ok = await confirm(
                u,
                '确认节点变更',
                u.h('div', {}, u.h('p', {}, '这次同步有些内容无法按原样使用。应用前请核对以下变化。'), review(u, r)),
                '应用此版本',
                () => syncHyper(u, feedback, r.confirmation_id),
                'ow-primary',
            );
            if (!ok && feedback) feedback.textContent = previous ? '已保留现有节点' : '已取消本次同步';
            return ok;
        }
        if (r?.decision !== 'ready') {
            const message = previous ? '新版本无法使用，已保留上次的节点' : '同步到的节点无法使用';
            if (feedback) {
                feedback.replaceChildren(u.h('details', {}, u.h('summary', {}, message), review(u, r)));
                feedback.dataset.tone = 'error';
            } else u.dialog(message, review(u, r));
            return false;
        }
        if (feedback) {
            const extra = r.notices?.length ? ' · ' + r.notices.length + ' 条提醒' : '';
            feedback.replaceChildren(u.h('details', {}, u.h('summary', {}, '同步完成 · ' + r.node_count + ' 个节点' + extra), review(u, r)));
        }
        return true;
    } catch (err) {
        const description = syncFailure(err);
        if (feedback) {
            feedback.textContent = '同步失败 · ' + description + (previous ? ' · 已保留上次的节点' : '');
            feedback.dataset.tone = 'error';
        }
        const failure = description !== err.message ? new Error(description) : err;
        if (feedback) failure.shown = feedback;
        throw failure;
    }
}

// --- Plans, amounts and dates as the Hyper app shows them ---------------------------------------

// The service's names can be a string, JSON text, or an object by language.
export function localizedText(value, fallback = '') {
    if (typeof value === 'string') {
        const text = value.trim();
        if (!text.startsWith('{')) return text || fallback;
        try {
            value = JSON.parse(text);
        } catch (_) {
            return fallback;
        }
    }
    if (!value || typeof value !== 'object' || Array.isArray(value)) return fallback;
    const entries = Object.entries(value);
    for (const key of ['zh-CN', 'zh_Hans', 'zh-Hans', 'zh', 'cn', 'en-US', 'en']) {
        const text = entries.find(([k]) => k.toLowerCase() === key.toLowerCase())?.[1];
        if (typeof text === 'string' && text.trim()) return text.trim();
    }
    const any = entries.find(([, v]) => typeof v === 'string' && v.trim());
    return any ? any[1].trim() : fallback;
}

// Hyper timestamps are milliseconds in newer builds and seconds in older ones.
export function toDate(ts) {
    const n = Number(ts);
    if (!n || n <= 0 || !Number.isFinite(n)) return null;
    return new Date(n > 1e12 ? n : n * 1000);
}
export function formatDate(ts) {
    const d = toDate(ts);
    if (!d) return '--';
    const p = (x) => String(x).padStart(2, '0');
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
}
export function expiryText(value, status = 1, now = Date.now()) {
    const n = typeof value === 'number' ? value : typeof value === 'string' && value.trim() ? Number(value) : NaN;
    const suffix = status === 1 ? '' : ' · 已失效';
    if (value === null || n === 0 || n === -62135596800000 || n === -62135596800) return '∞ 无限期' + suffix;
    if (!Number.isFinite(n) || n < 0) return '到期时间未知' + suffix;
    const ms = n > 1e12 ? n : n * 1000;
    const day = formatDate(ms);
    if (ms <= now) return day + ' 到期 · 已过期';
    return status === 1 ? day + ' 到期 · 剩余 ' + Math.ceil((ms - now) / 86400000) + ' 天' : day + ' 到期' + suffix;
}
const nonnegative = (v) => {
    const n = Number(v);
    return v != null && Number.isFinite(n) && n >= 0 ? n : null;
};
// A subscription's counters: bytes used and allowed, devices allowed.
export function membershipCounters(s) {
    const upload = nonnegative(s?.upload),
        download = nonnegative(s?.download);
    const devices = nonnegative(s?.subscribe?.device_limit);
    return {
        used: upload !== null && download !== null ? upload + download : null,
        total: nonnegative(s?.traffic),
        devices: devices !== null && Number.isInteger(devices) ? devices : null,
    };
}

export const DEFAULT_CURRENCY = { unit: 'USD', symbol: '$' };
export const MINOR_SCALE = 100;
const record = (v) => (v && typeof v === 'object' && !Array.isArray(v) ? v : {});
// Backend currency codes are uppercase letters and digits, at most 16 characters.
export function resolveCurrency(value, fallback = DEFAULT_CURRENCY) {
    const source = record(value);
    const rawUnit = typeof source.currency_unit === 'string' ? source.currency_unit.trim().toUpperCase() : '';
    const unit = /^[A-Z0-9]{1,16}$/.test(rawUnit) ? rawUnit : fallback.unit;
    const rawSymbol = typeof source.currency_symbol === 'string' ? source.currency_symbol.trim() : '';
    const symbol = rawSymbol && rawSymbol.length <= 16 ? rawSymbol : unit === fallback.unit ? fallback.symbol : unit;
    return { unit, symbol };
}
export const siteCurrency = (config) => resolveCurrency(record(config).currency);
export function amountScale(units) {
    const scale = units?.amount_scale;
    return typeof scale === 'number' && Number.isSafeInteger(scale) && scale > 0 ? scale : MINOR_SCALE;
}
// Account and order/pre amounts use scale 100; plan lists declare theirs. The backend's currency
// code is shown as it is, without conversion, as the mobile app does.
export function money(amount, currency = DEFAULT_CURRENCY, scale = MINOR_SCALE) {
    if (amount == null || !Number.isFinite(Number(amount))) return '--';
    const divisor = amountScale({ amount_scale: scale });
    const digits = Math.max(2, Math.ceil(Math.log10(divisor)));
    const value = new Intl.NumberFormat('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: digits }).format(Number(amount) / divisor);
    return value + ' ' + currency.unit;
}
export const UNIT_LABEL = { Year: '年', Month: '个月', Week: '周', Day: '天', Hour: '小时', NoLimit: '' };
// Order status, as the Hyper app reads it: 1 pending, 2/5 paid, 3 cancelled, 4 closed.
export function orderPhase(status) {
    if (status === 1) return 'pending';
    if (status === 2 || status === 5) return 'paid';
    if (status === 3 || status === 4) return 'closed';
    return 'unknown';
}
