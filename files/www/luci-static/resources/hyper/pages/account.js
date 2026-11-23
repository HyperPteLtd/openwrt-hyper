// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.
//
// The Hyper account: sign-in by email code, the plan, the account's node sync, invitations,
// signed-in devices, the daily check-in and announcements. The session stays on the router.
import { h, card, bytes, diagnosticMessage } from '../dom.js';
import {
    HYPER, account, api, apiError, read, write, syncHyper, pendingUpdates, actions, muted, badge, field, input, form, confirm, date,
    localizedText, expiryText, membershipCounters, siteCurrency, money, formatDate, DEFAULT_CURRENCY,
} from './common.js';

const emailPattern = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

export async function hyperAccount(ui) {
    const state = await account(ui);
    return state.signed_in ? signedIn(ui, state) : signIn(ui);
}

// --- Signing in -----------------------------------------------------------------------------------

function signIn(ui) {
    const memory = (ui.memory.signIn ||= { email: '', invite: '', step: 'email', cooldownUntil: 0 });
    const box = card('登录 Hyper');
    box.classList.add('ow-account-login');
    const line = ui.feedback(box, 'login');
    const email = input(ui, { type: 'email', required: true, autocomplete: 'email', placeholder: '输入邮箱地址', value: memory.email });
    const invite = input(ui, { type: 'text', autocomplete: 'off', placeholder: '输入邀请码', value: memory.invite, maxlength: 64 });
    const code = input(ui, { type: 'text', required: true, inputmode: 'numeric', autocomplete: 'one-time-code', placeholder: '邮件里的验证码', pattern: '[0-9]{4,8}' });
    const say = (text, tone = 'error') => {
        line.hidden = false;
        line.textContent = text;
        if (tone) line.dataset.tone = tone;
        else delete line.dataset.tone;
    };
    const sendCode = async () => {
        const address = email.value.trim();
        if (!emailPattern.test(address)) return say('请输入正确的邮箱地址');
        memory.email = address;
        memory.invite = invite.value.trim();
        const r = await write(ui, 'send_code', { email: address });
        if (!r?.result) return say(apiError(r, '验证码发送失败'));
        memory.step = 'code';
        memory.cooldownUntil = Date.now() + 60000;
        draw();
        say('验证码已发送到你的邮箱。没有账号会自动注册', null);
    };
    const signInNow = async () => {
        const value = code.value.trim();
        if (!/^\d{4,8}$/.test(value)) return say('请输入邮件里的验证码');
        const r = await write(ui, 'login', { email: memory.email, code: value, ...(memory.invite ? { invite: memory.invite } : {}) });
        if (!r?.result) return say(apiError(r, '登录失败'));
        ui.memory.signIn = null;
        ui.memory.justSignedIn = true;
        await ui.refresh();
    };
    const body = h('div', { class: 'ow-stack' });
    function draw() {
        if (memory.step === 'email') {
            body.replaceChildren(
                muted(ui, '使用邮箱登录或注册。登录凭证只保存在这台路由器上，不会留在浏览器里'),
                form(ui, [field(ui, '邮箱', email), field(ui, '邀请码（选填）', invite)], '获取验证码', sendCode),
            );
            return;
        }
        const resend = ui.button('重新发送验证码', sendCode, 'ow-link');
        const tickResend = () => {
            const left = Math.ceil((memory.cooldownUntil - Date.now()) / 1000);
            resend.disabled = left > 0;
            resend.textContent = left > 0 ? left + ' 秒后可重新发送' : '重新发送验证码';
            if (left > 0 && !ui.signal.aborted) setTimeout(tickResend, 1000);
        };
        tickResend();
        body.replaceChildren(
            h('div', { class: 'ow-row' }, h('strong', {}, memory.email), ui.button('更换邮箱', () => { memory.step = 'email'; draw(); }, 'ow-link')),
            form(ui, [field(ui, '验证码', code)], '登录', signInNow),
            actions(ui, resend),
        );
    }
    draw();
    box.append(body, line);
    return h('div', { class: 'ow-account' }, box);
}

// --- Signed in --------------------------------------------------------------------------------------

async function signedIn(ui, state) {
    const get = (path, query) => api(ui, 'GET', path, query ? { query } : {}).catch((e) => ({ ok: false, error: e.message }));
    const [info, subs, devices, site, affiliate, notices] = await Promise.all([
        get('/v1/public/user/info'),
        get('/v1/public/user/subscribe'),
        get('/v1/public/user/devices'),
        get('/v1/common/site/config'),
        get('/v1/public/user/affiliate/count'),
        get('/v1/public/announcement/list', { page: 1, size: 30 }),
    ]);
    if (ui.signal.aborted) return h('div');
    const currency = site?.ok ? siteCurrency(site.data) : DEFAULT_CURRENCY;
    const list = subs?.ok ? subs.data?.list || [] : [];
    const active = list.find((s) => s.status === 1) || list[0];
    return h(
        'div',
        { class: 'ow-account' },
        h('div', { class: 'ow-account-grid' }, profileCard(ui, state, info, active, subs), syncCard(ui)),
        h('div', { class: 'ow-account-grid' }, checkinCard(ui), inviteCard(ui, info, affiliate, currency)),
        devicesCard(ui, devices),
        announcementsCard(ui, notices),
    );
}

function profileCard(ui, state, info, active, subs) {
    const user = info?.ok ? info.data || {} : {};
    const name = user.nickname || state.email || '';
    const box = card('');
    box.classList.add('ow-account-profile');
    const line = ui.feedback(box, 'account');
    const logout = ui.button('退出登录', () =>
        confirm(ui, '退出登录', h('p', {}, '在这台路由器上退出登录？代理会停止，局域网恢复直连'), '退出登录', async () => {
            await write(ui, 'logout');
            if (ui.status.intent?.enabled) await ui.run('stop', { revision: ui.status.intent.revision }).catch(() => null);
            await ui.refresh();
        }), 'ow-link ow-danger-text');
    const metrics = membershipCounters(active);
    const planName = localizedText(active?.subscribe?.name, active ? '套餐' : '还没有套餐');
    box.append(
        h('div', { class: 'ow-row ow-account-identity' },
            h('span', { class: 'ow-avatar', 'aria-hidden': 'true' }, (Array.from(name)[0] || '?').toUpperCase()),
            h('div', { class: 'ow-stack ow-grow' }, h('strong', {}, name || '--'), muted(ui, state.email || '')),
            logout),
        h('div', { class: 'ow-row' },
            h('div', { class: 'ow-stack ow-grow' },
                h('strong', {}, planName),
                muted(ui, active ? expiryText(active.expire_time, active.status) : '购买套餐后，这台路由器就能连接')),
            ui.button(active ? '续费 / 升级' : '购买套餐', () => ui.go('plans'), 'ow-primary')),
        active
            ? h('dl', { class: 'ow-metrics' },
                h('div', {}, h('dt', {}, '已用流量'), h('dd', {}, metrics.used == null ? '--' : bytes(metrics.used))),
                h('div', {}, h('dt', {}, '总流量'), h('dd', {}, metrics.total === 0 ? '不限' : metrics.total == null ? '--' : bytes(metrics.total))),
                h('div', {}, h('dt', {}, '设备上限'), h('dd', {}, metrics.devices ?? '--')))
            : null,
        !subs?.ok ? muted(ui, apiError(subs, '套餐信息暂时无法读取')) : null,
        line,
    );
    return box;
}

// The account's nodes and rules on this router: when they were last read and how it went.
function syncCard(ui) {
    const s = (ui.sources || []).find((x) => x.source === HYPER);
    const box = card('节点同步');
    const line = ui.feedback(box, 'sync');
    const rows = [];
    if (!s || !s.ready) rows.push(muted(ui, ui.memory.justSignedIn ? '登录成功，正在同步节点…' : '还没有同步过节点'));
    else
        rows.push(
            h('div', { class: 'ow-row' },
                h('span', {}, h('strong', { class: 'ow-mono' }, String(s.node_count || 0)), ' 个节点'),
                muted(ui, s.updated_at ? '上次同步 ' + date(s.updated_at) : '尚未同步')),
        );
    const held = !s?.last_failure && s?.pending_update ? pendingUpdates[s.pending_update.code] : null;
    if (held) rows.push(h('p', { class: 'ow-warning' }, held.text));
    if (s?.login_required) rows.push(h('p', { class: 'ow-warning' }, '登录已失效，请退出后重新登录'));
    else if (s?.suspended) rows.push(h('p', { class: 'ow-warning' }, '套餐已到期或不可用，续费后再同步'));
    else if (s?.last_failure) rows.push(h('p', { class: 'ow-muted', 'data-tone': 'error' }, '上次同步失败 · ' + diagnosticMessage(s.last_failure) + (s.ready ? ' · 已保留上次的节点' : '')));
    const usage = s?.usage;
    if (usage?.total > 0) {
        const used = (Number(usage.upload) || 0) + (Number(usage.download) || 0);
        rows.push(h('progress', { value: Math.min(used, usage.total), max: usage.total, 'aria-label': '已用套餐流量' }),
            muted(ui, bytes(used) + ' / ' + bytes(usage.total) + (usage.expireAt ? ' · 到期 ' + formatDate(usage.expireAt) : '')));
    }
    const sync = ui.button(held ? held.action : '立即同步', async () => {
        if (await syncHyper(ui, line)) await ui.refresh();
    }, 'ow-primary');
    sync.dataset.focusKey = 'sync';
    box.append(...rows, actions(ui, sync), line);
    ui.memory.justSignedIn = false;
    return box;
}

function inviteCard(ui, info, affiliate, currency) {
    const user = info?.ok ? info.data || {} : {};
    const counts = affiliate?.ok ? affiliate.data || {} : {};
    const box = card('邀请好友');
    const status = h('span', { class: 'ow-muted', role: 'status' });
    const code = user.refer_code
        ? h('div', { class: 'ow-row' },
            h('strong', { class: 'ow-mono' }, user.refer_code),
            ui.button('复制', async () => {
                try {
                    await navigator.clipboard.writeText(user.refer_code);
                    status.textContent = '已复制';
                } catch (_) {
                    status.textContent = '请手动复制邀请码';
                }
                setTimeout(() => { status.textContent = ''; }, 1500);
            }, 'ow-link'),
            status)
        : muted(ui, '没有邀请码');
    box.append(
        muted(ui, '好友注册时填写你的邀请码，购买后你会获得返佣'),
        code,
        h('dl', { class: 'ow-metrics' },
            h('div', {}, h('dt', {}, '佣金'), h('dd', {}, money(user.commission, currency))),
            h('div', {}, h('dt', {}, '已邀请'), h('dd', {}, counts.registers ?? '--')),
            h('div', {}, h('dt', {}, '累计返佣'), h('dd', {}, money(counts.total_commission, currency)))),
    );
    return box;
}

function devicesCard(ui, devices) {
    const box = card('已登录的设备');
    const line = ui.feedback(box, 'devices');
    const items = devices?.ok ? devices.data?.list || [] : [];
    if (!devices?.ok) box.append(muted(ui, apiError(devices, '设备列表暂时无法读取')));
    else if (!items.length) box.append(muted(ui, '没有设备记录'));
    else
        box.append(h('ul', { class: 'ow-account-devices' }, ...items.map((d) => h('li', { class: 'ow-row' },
            h('strong', { class: 'ow-grow' }, d.current ? '本路由器' : d.platform || d.user_agent || '设备'),
            muted(ui, d.ip || '--'),
            badge(ui, d.online ? '在线' : '离线'),
            muted(ui, d.last_login_at ? '登录于 ' + formatDate(d.last_login_at) : '--'),
            d.current ? muted(ui, '当前') : ui.button('下线', async () => {
                const r = await api(ui, 'PUT', '/v1/public/user/device/kick_offline', { body: { id: d.id } });
                if (!r?.ok) throw new Error(apiError(r, '下线失败'));
                await ui.refresh();
            }, 'ow-link ow-danger-text')))));
    box.append(line);
    return box;
}

function announcementsCard(ui, notices) {
    const items = notices?.ok ? [...(notices.data?.announcements || [])] : [];
    if (!items.length) return null;
    items.sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned) || (b.created_at || 0) - (a.created_at || 0));
    return card('公告', ...items.slice(0, 10).map((n) =>
        h('details', { class: 'ow-account-notice' },
            h('summary', {}, n.pinned ? badge(ui, '置顶') : null, ' ', localizedText(n.title, '公告'), ' ', muted(ui, formatDate(n.created_at))),
            h('p', { class: 'ow-pre' }, localizedText(n.content, '')))));
}

// --- The daily check-in (kept on the router per account) --------------------------------------------

const pad = (n) => String(n).padStart(2, '0');
const dateKey = (d) => d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate());
const previousDay = (key) => {
    const [y, m, d] = key.split('-').map(Number);
    return dateKey(new Date(y, m - 1, d - 1, 12));
};
// Days in a row up to today, or up to yesterday while today is still open.
export function streak(checked, today) {
    let key = checked.has(today) ? today : previousDay(today);
    let n = 0;
    while (checked.has(key)) {
        n++;
        key = previousDay(key);
    }
    return n;
}
export function monthView(year, month, checked, today) {
    const first = new Date(year, month, 1, 12);
    const total = new Date(year, month + 1, 0, 12).getDate();
    const days = [];
    let count = 0;
    for (let day = 1; day <= total; day++) {
        const key = year + '-' + pad(month + 1) + '-' + pad(day);
        const done = checked.has(key);
        if (done) count++;
        const state = key === today ? (done ? 'today-checked' : 'today') : done ? 'checked' : key < today ? 'missed' : 'future';
        days.push({ key, day, state });
    }
    return { year, month, offset: (first.getDay() + 6) % 7, days, count };
}
const stateLabel = { checked: '已签到', 'today-checked': '今天，已签到', today: '今天，未签到', missed: '未签到', future: '' };

function checkinCard(ui) {
    const box = card('每日签到');
    box.classList.add('ow-checkin');
    const line = ui.feedback(box, 'checkin');
    const now = new Date();
    let view = [now.getFullYear(), now.getMonth()];
    let dates = [];
    const body = h('div', { class: 'ow-stack', 'aria-busy': 'true' });
    box.append(body, line);
    function draw() {
        const today = dateKey(new Date());
        const checked = new Set(dates);
        const done = checked.has(today);
        const month = monthView(view[0], view[1], checked, today);
        const earliest = dates.length ? dates.reduce((a, b) => (a < b ? a : b)) : today;
        const [fy, fm] = (earliest < today ? earliest : today).split('-').map(Number);
        const atFirst = view[0] * 12 + view[1] <= fy * 12 + fm - 1;
        const atLast = view[0] * 12 + view[1] >= now.getFullYear() * 12 + now.getMonth();
        const move = (delta) => {
            const d = new Date(view[0], view[1] + delta, 1, 12);
            view = [d.getFullYear(), d.getMonth()];
            draw();
        };
        const prev = ui.button('‹', () => move(-1), 'ow-link'),
            next = ui.button('›', () => move(1), 'ow-link');
        prev.disabled = atFirst;
        next.disabled = atLast;
        prev.setAttribute('aria-label', '上个月');
        next.setAttribute('aria-label', '下个月');
        const button = ui.button(done ? '今日已签到' : '立即签到', async () => {
            const r = await write(ui, 'checkin', { date: dateKey(new Date()) });
            if (!r?.result) throw new Error(diagnosticMessage(r?.error || '签到失败'));
            dates = r.dates || [...dates, dateKey(new Date())];
            view = [now.getFullYear(), now.getMonth()];
            draw();
            const n = streak(new Set(dates), dateKey(new Date()));
            line.hidden = false;
            delete line.dataset.tone;
            line.textContent = n > 1 ? '签到成功，已连续签到 ' + n + ' 天' : '签到成功';
        }, 'ow-primary');
        button.disabled = done;
        body.replaceChildren(
            h('div', { class: 'ow-row' },
                prev, h('strong', { 'aria-live': 'polite' }, month.year + ' 年 ' + (month.month + 1) + ' 月'), next,
                muted(ui, '本月已签到 ' + month.count + ' 天 · 连续 ' + streak(checked, today) + ' 天')),
            h('ol', { class: 'ow-calendar', 'aria-label': month.year + ' 年 ' + (month.month + 1) + ' 月签到记录' },
                ...Array.from({ length: month.offset }, () => h('li', { class: 'is-blank', 'aria-hidden': 'true' })),
                ...month.days.map((d) => h('li', {
                    class: 'is-' + d.state,
                    'aria-label': (month.month + 1) + ' 月 ' + d.day + ' 日' + (stateLabel[d.state] ? '，' + stateLabel[d.state] : ''),
                    'aria-current': d.state === 'today' || d.state === 'today-checked' ? 'date' : null,
                }, String(d.day)))),
            actions(ui, button),
        );
        body.setAttribute('aria-busy', 'false');
    }
    read(ui, 'checkin_status').then((r) => {
        if (ui.signal.aborted) return;
        if (r?.result) dates = r.dates || [];
        draw();
    }).catch((e) => {
        if (ui.signal.aborted) return;
        draw();
        line.hidden = false;
        line.textContent = diagnosticMessage(e.message);
        line.dataset.tone = 'error';
    });
    return box;
}
