// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.
//
// Plans and payment, the way the Hyper app pays (payment-v2):
//   order/pre (price preview) -> v2/orders/purchase -> the payment action (QR code, payment page,
//   crypto transfer) -> v2/orders/{no}/payment, polled until the order is paid or closed.
// Writes carry an idempotency key, so a repeated press or a retry never creates a second order.
// The open order survives a reload of this tab (sessionStorage).
import { say, empty, diagnosticMessage } from '../dom.js';
import {
    api, apiError, ids, actions, muted, badge, input, localizedText, siteCurrency, resolveCurrency, amountScale,
    money, UNIT_LABEL, DEFAULT_CURRENCY, syncHyper,
} from './common.js';

// --- payment-v2 -----------------------------------------------------------------------------------

export const ORDER_ALREADY_PAID = 62035;
export const SESSION_RECOVERING = 62030;
const PREPARING_WINDOW_MS = 15000;

const paymentErrors = {
    62025: '支付汇率暂不可用，请稍后重试',
    62026: '当前支付渠道不可用',
    62036: '当前支付渠道不可用',
    62027: '支付订单已失效',
    62029: '当前支付订单仍可继续，请勿重复下单',
    62030: '支付订单正在恢复，请稍后刷新',
    62032: '支付请求无法安全恢复，请重新操作',
    62033: '支付请求无法安全恢复，请重新操作',
    62034: '当前币种或网络不可用',
    62037: '当前币种或网络不可用',
    62035: '订单已支付，正在同步权益',
};

// Upstream branding is an implementation detail, in the service's messages too.
const product = (text) => String(text ?? '').replace(/ppanel/gi, 'Hyper');
const failure = (r, fallback) => product(apiError(r, fallback));

// The text for a payment call that failed: the router's error, the payment code, or the service's
// own words when they are short and carry nothing sensitive.
export function paymentError(r, fallback) {
    if (r?.error === 'api_unavailable') return '暂时无法连接服务，请稍后查看订单状态';
    if (r?.error) {
        const known = diagnosticMessage(r.error);
        if (known && known !== r.error) return known;
    }
    const known = r?.code != null ? paymentErrors[r.code] : undefined;
    if (known) return known;
    const msg = typeof r?.msg === 'string' ? r.msg.trim() : '';
    if (msg && msg.length <= 240 && !/(https?:\/\/|authorization|bearer\s|client_secret|publishable_key)/i.test(msg)) return product(msg);
    return fallback;
}

// An idempotency key in the app's shape, checkout:v2:<UUID v4> (crypto.randomUUID needs HTTPS;
// LuCI is often plain HTTP).
export function checkoutKey() {
    const hex = (ids() + ids()).slice(0, 32).split('');
    hex[12] = '4';
    hex[16] = '89ab'[parseInt(hex[16], 16) & 3];
    const s = hex.join('');
    return 'checkout:v2:' + s.slice(0, 8) + '-' + s.slice(8, 12) + '-' + s.slice(12, 16) + '-' + s.slice(16, 20) + '-' + s.slice(20);
}

// Apple in-app purchase only works inside the iOS app.
export function isAppleIap(m) {
    const text = (v) => (typeof v === 'string' ? v : v && typeof v === 'object' ? JSON.stringify(v) : '');
    const meta = [m?.platform, m?.name, m?.description].map(text).join(' ').toLowerCase().replace(/[_\-.]+/g, ' ').replace(/\s+/g, ' ');
    return /apple\s+(?:iap|in\s+app|purchase)|app\s+store|苹果(?:内购|应用内购买)/.test(meta);
}

export function isSession(v) {
    return !!v && typeof v.order_no === 'string' && !!v.order_no && typeof v.payment_action === 'object' && !!v.payment_action;
}
export function isPaid(s) {
    return !!s && (s.payment_action.state === 'paid' || s.order_status === 'paid' || s.order_status === 'fulfilled');
}
export function isTerminal(s) {
    if (!s) return false;
    return ['paid', 'fulfilled', 'cancelled', 'expired', 'failed'].includes(s.order_status) ||
        ['paid', 'cancelled', 'expired', 'failed'].includes(s.payment_action.state);
}
export function isPreparing(s) {
    return !isTerminal(s) && (s.payment_action.type === 'preparing' || s.payment_action.state === 'preparing');
}
// The next poll in ms, or null to stop: the order is over, or its payment has been preparing for
// too long (the page then offers a retry).
export function nextPollDelay(s, preparingSince, now) {
    if (!s || isTerminal(s)) return null;
    if (isPreparing(s)) {
        if (preparingSince != null && now - preparingSince >= PREPARING_WINDOW_MS) return null;
        const wanted = s.payment_action.type === 'preparing' ? s.payment_action.retry_after_ms ?? 1000 : 1000;
        return Math.max(250, Math.min(5000, wanted));
    }
    return 5000;
}
export function remainingSeconds(s, now) {
    const at = s?.payment_action.expires_at;
    return at ? Math.max(0, Math.ceil((at * 1000 - now) / 1000)) : null;
}
export const formatRemaining = (sec) => String(Math.floor(sec / 60)).padStart(2, '0') + ':' + String(sec % 60).padStart(2, '0');

const SYMBOL = { CNY: '¥', USD: '$', EUR: '€', HKD: 'HK$', JPY: '¥', GBP: '£' };
// The order's amounts are decimal strings in their currency.
export function decimalMoney(amount, currency) {
    if (!amount || !currency) return null;
    const sym = SYMBOL[String(currency).toUpperCase()];
    return sym ? sym + amount : amount + ' ' + String(currency).toUpperCase();
}
// What the payer actually sends on the chosen channel.
export function channelAmount(s) {
    const a = s.payment_action;
    if (a.type === 'crypto_transfer' && a.actual_amount && a.token) return a.actual_amount + ' ' + a.token;
    return decimalMoney(a.payment_amount, a.payment_currency);
}
// Only https:// opens from the page; a wallet scheme can still be shown as a QR code.
export function httpsTarget(value) {
    const raw = typeof value === 'string' ? value.trim() : '';
    if (!raw || /[\u0000- \u007f\\]/.test(raw)) return null;
    try {
        const u = new URL(raw);
        if (u.protocol !== 'https:' || u.username || u.password) return null;
        return u.toString();
    } catch (_) {
        return null;
    }
}
export function walletTarget(value) {
    const raw = typeof value === 'string' ? value.trim() : '';
    return /^(alipay|alipays|weixin|wxp):\/\/[^\s]+$/i.test(raw) ? raw : null;
}

const orderPath = (no, tail) => '/v2/orders/' + encodeURIComponent(no) + '/' + tail;

// --- The open order, kept for this tab ----------------------------------------------------------

const PENDING_KEY = 'hyper.order.pending';
function savePending(p) {
    try {
        if (p) sessionStorage.setItem(PENDING_KEY, JSON.stringify(p));
        else sessionStorage.removeItem(PENDING_KEY);
    } catch (_) {
        // Storage blocked: the order is just not resumed after a reload.
    }
}
function loadPending() {
    try {
        const p = JSON.parse(sessionStorage.getItem(PENDING_KEY) || 'null');
        if (p && typeof p.order_no === 'string' && p.order_no && Date.now() - p.time < 86400e3) return p;
    } catch (_) {}
    return null;
}

// Copy, with a fallback for plain-HTTP LuCI where navigator.clipboard is missing.
async function copyText(text) {
    try {
        if (navigator.clipboard && window.isSecureContext) {
            await navigator.clipboard.writeText(text);
            return true;
        }
    } catch (_) {}
    try {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('readonly', '');
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.append(ta);
        ta.select();
        const ok = document.execCommand('copy');
        ta.remove();
        return ok;
    } catch (_) {
        return false;
    }
}

// --- QR codes -------------------------------------------------------------------------------------
// A payment link or address as a QR code: byte mode, error correction level M, the smallest version
// that holds it and the mask with the lowest penalty (ISO/IEC 18004), as the app draws it.

const QR_ECC = [-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28];
const QR_BLOCKS = [-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49];
const QR_MASKS = [
    (x, y) => (x + y) % 2 === 0,
    (x, y) => y % 2 === 0,
    (x) => x % 3 === 0,
    (x, y) => (x + y) % 3 === 0,
    (x, y) => (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0,
    (x, y) => ((x * y) % 2) + ((x * y) % 3) === 0,
    (x, y) => (((x * y) % 2) + ((x * y) % 3)) % 2 === 0,
    (x, y) => (((x + y) % 2) + ((x * y) % 3)) % 2 === 0,
];
function qrRawModules(ver) {
    let n = (16 * ver + 128) * ver + 64;
    if (ver >= 2) {
        const align = Math.floor(ver / 7) + 2;
        n -= (25 * align - 10) * align - 55;
        if (ver >= 7) n -= 36;
    }
    return n;
}
const qrDataCodewords = (ver) => Math.floor(qrRawModules(ver) / 8) - QR_ECC[ver] * QR_BLOCKS[ver];
function gfMul(x, y) {
    let z = 0;
    for (let i = 7; i >= 0; i--) {
        z = (z << 1) ^ ((z >>> 7) * 0x11d);
        z ^= ((y >>> i) & 1) * x;
    }
    return z;
}
function rsDivisor(degree) {
    const result = new Array(degree).fill(0);
    result[degree - 1] = 1;
    let root = 1;
    for (let i = 0; i < degree; i++) {
        for (let j = 0; j < degree; j++) {
            result[j] = gfMul(result[j], root);
            if (j + 1 < degree) result[j] ^= result[j + 1];
        }
        root = gfMul(root, 2);
    }
    return result;
}
function rsRemainder(data, divisor) {
    const result = divisor.map(() => 0);
    for (const b of data) {
        const factor = b ^ result.shift();
        result.push(0);
        divisor.forEach((c, i) => {
            result[i] ^= gfMul(c, factor);
        });
    }
    return result;
}
function qrPenalty(m) {
    const size = m.length;
    let result = 0;
    const lines = (at) => {
        for (let a = 0; a < size; a++) {
            const history = [0, 0, 0, 0, 0, 0, 0];
            const add = (run) => {
                history.pop();
                history.unshift(history[0] === 0 ? run + size : run);
            };
            const finders = () => {
                const n = history[1];
                const core = n > 0 && history[2] === n && history[3] === n * 3 && history[4] === n && history[5] === n;
                return (core && history[0] >= n * 4 && history[6] >= n ? 1 : 0) + (core && history[6] >= n * 4 && history[0] >= n ? 1 : 0);
            };
            let color = false,
                run = 0;
            for (let b = 0; b < size; b++) {
                if (at(a, b) === color) {
                    run++;
                    if (run === 5) result += 3;
                    else if (run > 5) result++;
                } else {
                    add(run);
                    if (!color) result += finders() * 40;
                    color = at(a, b);
                    run = 1;
                }
            }
            if (color) {
                add(run);
                run = 0;
            }
            add(run + size);
            result += finders() * 40;
        }
    };
    lines((y, x) => m[y][x]);
    lines((x, y) => m[y][x]);
    for (let y = 0; y < size - 1; y++)
        for (let x = 0; x < size - 1; x++) {
            const c = m[y][x];
            if (c === m[y][x + 1] && c === m[y + 1][x] && c === m[y + 1][x + 1]) result += 3;
        }
    let dark = 0;
    for (const row of m) for (const c of row) if (c) dark++;
    const total = size * size;
    return result + (Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1) * 10;
}
// The modules of text's QR code, rows of booleans (true is dark) without a quiet zone, or null when
// the text is too long for one.
export function qrMatrix(text) {
    const bytes = new TextEncoder().encode(String(text));
    const countBits = (ver) => (ver < 10 ? 8 : 16);
    let ver = 1;
    while (ver <= 40 && 4 + countBits(ver) + bytes.length * 8 > qrDataCodewords(ver) * 8) ver++;
    if (ver > 40) return null;
    // Mode, length, the bytes, terminator and padding.
    const capacity = qrDataCodewords(ver) * 8;
    const bits = [];
    const put = (value, len) => {
        for (let i = len - 1; i >= 0; i--) bits.push((value >>> i) & 1);
    };
    put(4, 4);
    put(bytes.length, countBits(ver));
    for (const b of bytes) put(b, 8);
    put(0, Math.min(4, capacity - bits.length));
    put(0, (8 - (bits.length % 8)) % 8);
    for (let pad = 0xec; bits.length < capacity; pad ^= 0xec ^ 0x11) put(pad, 8);
    const data = [];
    for (let i = 0; i < bits.length; i += 8) data.push(bits.slice(i, i + 8).reduce((a, b) => (a << 1) | b, 0));
    // Error correction per block, the blocks interleaved.
    const blocks = QR_BLOCKS[ver],
        eccLen = QR_ECC[ver],
        raw = Math.floor(qrRawModules(ver) / 8);
    const short = blocks - (raw % blocks),
        shortLen = Math.floor(raw / blocks),
        divisor = rsDivisor(eccLen);
    const parts = [];
    for (let i = 0, k = 0; i < blocks; i++) {
        const dat = data.slice(k, k + shortLen - eccLen + (i < short ? 0 : 1));
        k += dat.length;
        const ecc = rsRemainder(dat, divisor);
        if (i < short) dat.push(0);
        parts.push(dat.concat(ecc));
    }
    const codewords = [];
    for (let i = 0; i < parts[0].length; i++)
        parts.forEach((part, j) => {
            if (i !== shortLen - eccLen || j >= short) codewords.push(part[i]);
        });
    // Finder, timing and alignment patterns, format and version information.
    const size = ver * 4 + 17;
    const dark = Array.from({ length: size }, () => new Array(size).fill(false));
    const fixed = Array.from({ length: size }, () => new Array(size).fill(false));
    const set = (x, y, on) => {
        dark[y][x] = on;
        fixed[y][x] = true;
    };
    for (let i = 0; i < size; i++) {
        set(6, i, i % 2 === 0);
        set(i, 6, i % 2 === 0);
    }
    for (const [cx, cy] of [[3, 3], [size - 4, 3], [3, size - 4]])
        for (let dy = -4; dy <= 4; dy++)
            for (let dx = -4; dx <= 4; dx++) {
                const x = cx + dx,
                    y = cy + dy,
                    dist = Math.max(Math.abs(dx), Math.abs(dy));
                if (x >= 0 && x < size && y >= 0 && y < size) set(x, y, dist !== 2 && dist !== 4);
            }
    const align = [];
    if (ver > 1) {
        const count = Math.floor(ver / 7) + 2;
        const step = ver === 32 ? 26 : Math.ceil((ver * 4 + 4) / (count * 2 - 2)) * 2;
        align.push(6);
        for (let pos = size - 7; align.length < count; pos -= step) align.splice(1, 0, pos);
    }
    const last = align.length - 1;
    align.forEach((ax, i) =>
        align.forEach((ay, j) => {
            if ((i === 0 && j === 0) || (i === 0 && j === last) || (i === last && j === 0)) return;
            for (let dy = -2; dy <= 2; dy++)
                for (let dx = -2; dx <= 2; dx++) set(ax + dx, ay + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
        }),
    );
    // Level M is 00, so the format data is the mask alone.
    const format = (mask) => {
        let rem = mask;
        for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
        const word = ((mask << 10) | rem) ^ 0x5412;
        const bit = (i) => ((word >>> i) & 1) !== 0;
        for (let i = 0; i <= 5; i++) set(8, i, bit(i));
        set(8, 7, bit(6));
        set(8, 8, bit(7));
        set(7, 8, bit(8));
        for (let i = 9; i < 15; i++) set(14 - i, 8, bit(i));
        for (let i = 0; i < 8; i++) set(size - 1 - i, 8, bit(i));
        for (let i = 8; i < 15; i++) set(8, size - 15 + i, bit(i));
        set(8, size - 8, true);
    };
    format(0);
    if (ver >= 7) {
        let rem = ver;
        for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
        const word = (ver << 12) | rem;
        for (let i = 0; i < 18; i++) {
            const on = ((word >>> i) & 1) !== 0,
                a = size - 11 + (i % 3),
                b = Math.floor(i / 3);
            set(a, b, on);
            set(b, a, on);
        }
    }
    // The codewords, in two-column zigzags from the bottom right.
    let n = 0;
    for (let right = size - 1; right >= 1; right -= 2) {
        if (right === 6) right = 5;
        for (let vert = 0; vert < size; vert++)
            for (let j = 0; j < 2; j++) {
                const x = right - j,
                    y = ((right + 1) & 2) === 0 ? size - 1 - vert : vert;
                if (!fixed[y][x] && n < codewords.length * 8) {
                    dark[y][x] = ((codewords[n >>> 3] >>> (7 - (n & 7))) & 1) !== 0;
                    n++;
                }
            }
    }
    const applyMask = (m) => {
        for (let y = 0; y < size; y++)
            for (let x = 0; x < size; x++) if (!fixed[y][x] && QR_MASKS[m](x, y)) dark[y][x] = !dark[y][x];
    };
    let best = 0,
        lowest = Infinity;
    for (let m = 0; m < 8; m++) {
        applyMask(m);
        format(m);
        const penalty = qrPenalty(dark);
        if (penalty < lowest) {
            best = m;
            lowest = penalty;
        }
        applyMask(m);
    }
    applyMask(best);
    format(best);
    return dark;
}

// --- The page ---------------------------------------------------------------------------------------

const stepNames = ['选择套餐', '确认订单', '支付', '完成'];
const stepIndex = { pick: 0, confirm: 1, order: 2, done: 3, closed: 2 };
const transient = (r) => !!r?.thrown || r?.error === 'api_unavailable' || r?.error === 'timeout';
const unitOf = (plan) => UNIT_LABEL[plan?.unit_time || ''] ?? plan?.unit_time ?? '';

export async function plans(ui) {
    const h = ui.h;
    // A call that failed on the router reads like one the service refused.
    const call = (method, path, opts) => api(ui, method, path, opts).catch((e) => ({ ok: false, error: e?.message || String(e), thrown: true }));
    const getPayment = (no) => call('GET', orderPath(no, 'payment'));
    const pending = loadPending();
    const [site, list, methods, restored] = await Promise.all([
        call('GET', '/v1/common/site/config'),
        call('GET', '/v1/public/subscribe/list'),
        call('GET', '/v1/public/payment/methods'),
        pending ? getPayment(pending.order_no) : null,
    ]);
    if (ui.signal.aborted) return h('div');

    // The selection outlives a redraw of the page; the rest lasts while the page is shown.
    const sel = (ui.memory.plans ||= { plan: 0, quantity: 0, payment: 0, coupon: '' });
    const st = {
        step: 'pick',
        currency: site?.ok ? siteCurrency(site.data) : DEFAULT_CURRENCY,
        plans: [],
        units: {},
        methods: [],
        preview: null,
        // The selection the preview priced; the order is placed for it.
        confirmed: null,
        purchaseKey: '',
        session: null,
        preparingSince: null,
        stalled: false,
        // Why polling stopped, when the service refused to report on the order.
        halted: '',
        payPlan: '',
        duration: '',
        asset: { network: '', token: '', label: '' },
        busy: false,
        synced: false,
    };
    if (list?.ok) {
        st.plans = (Array.isArray(list.data?.list) ? list.data.list : [])
            .filter((p) => p && Array.isArray(p.discount) && p.discount.length > 0)
            .map((p) => ({ ...p, name: localizedText(p.name, '套餐'), description: localizedText(p.description) }));
        st.units = list.data?.units || {};
    }
    if (methods?.ok) {
        st.methods = (Array.isArray(methods.data?.list) ? methods.data.list : [])
            .filter((m) => m && !isAppleIap(m))
            .map((m) => ({ ...m, name: localizedText(m.name, '支付方式'), description: localizedText(m.description) }));
    }
    const planOf = (id) => st.plans.find((p) => p.id === id);
    const chosen = planOf(sel.plan) || st.plans[0];
    if (chosen) {
        const tier = chosen.discount.find((t) => t.quantity === sel.quantity) || chosen.discount.find((t) => t.recommend) || chosen.discount[0];
        sel.plan = chosen.id;
        sel.quantity = tier.quantity;
    }
    if (!st.methods.some((m) => m.id === sel.payment)) sel.payment = st.methods[0]?.id ?? 0;

    const steps = h('ol', { class: 'ow-plans-steps', 'aria-label': '购买步骤' });
    const root = h(
        'div',
        { class: 'ow-plans' },
        h('div', { class: 'ow-row ow-plans-head' }, h('a', { href: '#account', class: 'ow-plans-back' }, '‹ 返回账户'), steps),
    );
    const line = ui.feedback(root, 'plans');
    const body = h('div', { class: 'ow-stack' });
    root.append(line, body);
    // The node sync after a payment reports here.
    const syncLine = h('p', { class: 'ow-feedback', role: 'status' });
    const fail = (text) => say(line, text, { tone: 'error' });

    let drawn = '',
        timer = 0,
        ticker = 0,
        copyTimer = 0,
        rev = 0,
        previews = 0,
        cancelDialog = null;
    ui.signal.addEventListener(
        'abort',
        () => {
            clearTimeout(timer);
            clearInterval(ticker);
            clearTimeout(copyTimer);
            cancelDialog?.close();
        },
        { once: true },
    );

    // One action at a time: while one runs, the page's other controls ignore presses. An action's
    // error goes to the line at the top, as the redraw may have replaced its control.
    function lock(on) {
        for (const b of body.querySelectorAll('[data-lock]')) {
            if (b.hasAttribute('aria-busy')) continue;
            if (on) b.setAttribute('aria-disabled', 'true');
            else b.removeAttribute('aria-disabled');
        }
    }
    const act = (fn, rethrow = false) => async (ev) => {
        if (st.busy) return;
        st.busy = true;
        lock(true);
        try {
            await fn(ev);
        } catch (e) {
            if (rethrow) throw e;
            if (e?.name !== 'AbortError' && !ui.signal.aborted) fail(product(diagnosticMessage(e?.message ?? String(e))) || '操作失败');
        } finally {
            st.busy = false;
            if (!ui.signal.aborted) lock(false);
        }
    };
    const control = (text, fn, cls = '', key = '') => {
        const b = ui.button(text, act(fn), cls);
        b.dataset.lock = '';
        if (key) b.dataset.focusKey = key;
        return b;
    };
    const radio = (content, on, key, pick, cls = 'ow-plans-option') =>
        h(
            'button',
            {
                type: 'button',
                role: 'radio',
                'aria-checked': String(on),
                class: cls,
                'data-lock': '',
                'data-focus-key': key,
                onclick: () => {
                    if (st.busy) return;
                    pick();
                    draw();
                },
            },
            content,
        );
    const fact = (label, value, strong = false) => [h('dt', {}, label), h('dd', { class: strong ? 'ow-plans-strong' : null }, value)];

    // --- Drawing ---

    function draw() {
        if (ui.signal.aborted) return;
        const active = document.activeElement;
        const within = body.contains(active);
        const key = within ? active.dataset?.focusKey : '';
        const moved = drawn && drawn !== st.step && (within || !active || active === document.body);
        const index = stepIndex[st.step];
        steps.replaceChildren(
            ...stepNames.map((name, i) =>
                h('li', { class: i < index ? 'is-done' : null, 'aria-current': i === index ? 'step' : null }, i + 1 + ' ' + name),
            ),
        );
        body.replaceChildren(view());
        lock(st.busy);
        drawn = st.step;
        if (st.step !== 'order') cancelDialog?.close();
        clock();
        if (st.step === 'done' && !st.synced) {
            // Paid: the new plan's nodes come onto the router now.
            st.synced = true;
            syncHyper(ui, syncLine).catch(() => {
                // The line shows the failure.
            });
        }
        if (moved) body.querySelector('h2')?.focus();
        else if (key) body.querySelector('[data-focus-key="' + CSS.escape(key) + '"]')?.focus({ preventScroll: true });
    }
    function view() {
        if (st.step === 'confirm' && st.confirmed && planOf(st.confirmed.plan)) return confirmView();
        if (st.step === 'order' && st.session) return orderView();
        if (st.step === 'done') return doneView();
        if (st.step === 'closed') return closedView();
        return pickView();
    }
    function go(step) {
        if (step === 'done') savePending(null);
        st.step = step;
        draw();
    }

    function pickView() {
        const box = h('section', { class: 'ow-card ow-plans-pick' }, h('h2', { tabindex: -1 }, '选择会员套餐'));
        const scale = amountScale(st.units),
            currency = resolveCurrency(st.units, st.currency);
        for (const p of st.plans) {
            const unit = unitOf(p);
            box.append(
                h(
                    'div',
                    { class: 'ow-stack ow-plans-plan' },
                    h('div', { class: 'ow-plans-plan-head' }, h('h3', {}, p.name), p.device_limit ? muted(ui, p.device_limit + ' 台设备') : null),
                    h(
                        'div',
                        { class: 'ow-plans-tiers', role: 'radiogroup', 'aria-label': p.name },
                        p.discount.map((t) =>
                            radio(
                                [
                                    h('span', {}, t.quantity + ' ' + unit),
                                    h('strong', {}, money(t.sell_price, currency, scale)),
                                    t.original_price && t.original_price > t.sell_price ? h('s', { class: 'ow-muted' }, money(t.original_price, currency, scale)) : null,
                                    t.recommend ? badge(ui, '推荐') : null,
                                ],
                                sel.plan === p.id && sel.quantity === t.quantity,
                                'tier:' + p.id + ':' + t.quantity,
                                () => {
                                    sel.plan = p.id;
                                    sel.quantity = t.quantity;
                                },
                                'ow-plans-tier',
                            ),
                        ),
                    ),
                ),
            );
        }
        if (!st.plans.length) {
            if (list?.ok) box.append(empty('暂时没有可购买的套餐'));
            else box.append(actions(ui, ui.button('重新读取', () => ui.refresh())));
        }
        const plan = planOf(sel.plan),
            tier = plan?.discount.find((t) => t.quantity === sel.quantity);
        if (plan && tier) {
            const buy = control('购买', toConfirm, 'ow-primary', 'buy');
            if (!st.methods.length) buy.disabled = true;
            box.append(
                h(
                    'div',
                    { class: 'ow-plans-summary' },
                    muted(ui, plan.name + ' · ' + tier.quantity + ' ' + unitOf(plan)),
                    h('strong', { class: 'ow-plans-price' }, money(tier.sell_price, currency, scale)),
                    buy,
                ),
            );
        }
        if (!st.methods.length && st.plans.length) box.append(muted(ui, '没有可用的支付方式'));
        return box;
    }

    function confirmView() {
        const c = st.confirmed,
            plan = planOf(c.plan),
            p = st.preview || {},
            currency = st.currency;
        const coupon = input(ui, { type: 'text', value: sel.coupon, placeholder: '输入优惠码', autocomplete: 'off', 'aria-label': '优惠码', 'data-focus-key': 'coupon' });
        coupon.addEventListener('input', () => {
            sel.coupon = coupon.value;
        });
        const apply = control('使用', toConfirm, '', 'apply');
        coupon.addEventListener('keydown', (e) => {
            if (e.key !== 'Enter') return;
            e.preventDefault();
            apply.click();
        });
        const methodList = h(
            'fieldset',
            { class: 'ow-plans-options', role: 'radiogroup' },
            h('legend', { class: 'ow-muted' }, '支付方式'),
            st.methods.map((m) => {
                const b = control(
                    '',
                    async () => {
                        sel.payment = m.id;
                        for (const other of methodList.querySelectorAll('[role="radio"]')) other.setAttribute('aria-checked', String(other === b));
                        await toConfirm();
                    },
                    'ow-plans-option',
                    'method:' + m.id,
                );
                b.setAttribute('role', 'radio');
                b.setAttribute('aria-checked', String(sel.payment === m.id));
                b.replaceChildren(h('span', { class: 'ow-plans-option-text' }, h('span', {}, m.name), m.description ? muted(ui, m.description) : null));
                return b;
            }),
        );
        const order = h(
            'section',
            { class: 'ow-card' },
            h('h2', { tabindex: -1 }, '确认订单'),
            h(
                'dl',
                { class: 'ow-facts ow-plans-facts' },
                fact('套餐', plan.name),
                fact('时长', c.quantity + ' ' + unitOf(plan)),
                fact('原价', money(p.price, currency)),
                p.discount ? fact('优惠', '-' + money(p.discount, currency)) : null,
                p.coupon_discount ? fact('优惠码', '-' + money(p.coupon_discount, currency)) : null,
                p.fee_amount ? fact('手续费', money(p.fee_amount, currency)) : null,
            ),
            h('div', { class: 'ow-field' }, h('span', {}, '优惠码'), h('div', { class: 'ow-plans-coupon' }, coupon, apply)),
            methodList,
        );
        const due = h(
            'section',
            { class: 'ow-card ow-plans-due' },
            muted(ui, '应付'),
            h('strong', { class: 'ow-plans-price' }, money(p.amount, currency)),
            muted(ui, '点“去支付”后生成订单：扫码支付会在这里显示二维码，其他方式会给出支付页面或收款地址。这个页面会自动检查支付结果'),
            control('去支付', payNow, 'ow-primary', 'pay'),
            control('返回修改', () => go('pick'), '', 'back'),
        );
        return h('div', { class: 'ow-plans-split' }, order, due);
    }

    function orderView() {
        const s = st.session,
            a = s.payment_action;
        const left = remainingSeconds(s, Date.now());
        const pay = h(
            'section',
            { class: 'ow-card ow-plans-pay' },
            h(
                'div',
                { class: 'ow-plans-wait' },
                h('h2', { tabindex: -1 }, '等待支付'),
                muted(ui, '完成支付后这里会自动更新，路由器随后同步新的节点'),
                left != null ? h('strong', { class: 'ow-plans-clock', role: 'timer', 'aria-label': '剩余支付时间' }) : null,
            ),
            st.halted
                ? h('div', { class: 'ow-plans-center' }, h('p', { class: 'ow-warning', role: 'alert' }, st.halted), control('重新检查', resume, '', 'resume'))
                : null,
            paymentAction(s, a),
        );
        const amount = decimalMoney(s.amount, s.currency),
            paid = channelAmount(s);
        const order = h(
            'section',
            { class: 'ow-card' },
            h('h2', {}, '订单'),
            h(
                'dl',
                { class: 'ow-facts ow-plans-facts' },
                fact('订单号', h('span', { class: 'ow-mono' }, s.order_no)),
                st.payPlan ? fact('套餐', st.payPlan) : null,
                st.duration ? fact('时长', st.duration) : null,
                amount ? fact('订单金额', amount) : null,
                paid ? fact('实付', paid, true) : null,
            ),
            actions(ui, control('取消订单', askCancel, 'ow-link ow-danger', 'cancel')),
        );
        return h('div', { class: 'ow-plans-split' }, pay, order);
    }

    function paymentAction(s, a) {
        const note = (text, live = false) => h('p', { class: 'ow-muted', role: live ? 'status' : null }, text);
        switch (a.type) {
            case 'preparing':
                return st.stalled
                    ? h('div', { class: 'ow-plans-center' }, note('支付参数还没准备好'), control('重试', retryPreparing, 'ow-primary', 'retry'))
                    : note('正在生成支付参数…', true);
            case 'qr':
                if (!a.qr_content) break;
                return h('div', { class: 'ow-plans-center' }, qrCode(a.qr_content, '支付二维码'), note('请用手机扫码完成支付'));
            case 'redirect': {
                const https = httpsTarget(a.provider_url) ?? httpsTarget(a.deeplink),
                    wallet = walletTarget(a.deeplink);
                if (!https && !wallet) return note('支付链接无效，请取消订单后换一种支付方式');
                return h(
                    'div',
                    { class: 'ow-plans-center' },
                    https ? payLink(https) : null,
                    qrCode(https ?? wallet, '支付链接二维码', 180),
                    note(https ? '也可以用手机扫码，在手机上完成支付' : '请用手机扫码，在手机上完成支付'),
                );
            }
            case 'stripe': {
                const https = httpsTarget(a.provider_url);
                return https ? h('div', { class: 'ow-plans-center' }, payLink(https)) : note('这种支付方式请在 Hyper App 里完成，或取消订单后换一种支付方式');
            }
            case 'crypto_selection':
                return cryptoSelection(a);
            case 'crypto_transfer':
                if (!a.receive_address) break;
                return cryptoTransfer(a);
            case 'apple_iap':
                return note('Apple 内购只能在 iPhone / iPad 的 Hyper App 里完成。可以取消这个订单，换一种支付方式');
        }
        // 'none', or an action whose details have not arrived yet: the next poll brings them.
        return note('正在同步支付状态…', true);
    }
    const payLink = (url) => h('a', { class: 'ow-plans-open', href: url, target: '_blank', rel: 'noopener noreferrer' }, '打开支付页面');

    function qrCode(value, label, size = 220) {
        const matrix = qrMatrix(value);
        if (!matrix) return muted(ui, '内容太长，无法生成二维码');
        const n = matrix.length,
            pad = 4,
            ns = 'http://www.w3.org/2000/svg';
        let d = '';
        matrix.forEach((row, y) => {
            for (let x = 0; x < n; x++) {
                if (!row[x]) continue;
                let run = 1;
                while (x + run < n && row[x + run]) run++;
                d += 'M' + x + ' ' + y + 'h' + run + 'v1h-' + run + 'z';
                x += run - 1;
            }
        });
        const node = (tag, attrs) => {
            const el = document.createElementNS(ns, tag);
            for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
            return el;
        };
        const svg = node('svg', {
            role: 'img',
            'aria-label': label,
            width: size,
            height: size,
            viewBox: -pad + ' ' + -pad + ' ' + (n + pad * 2) + ' ' + (n + pad * 2),
            'shape-rendering': 'crispEdges',
        });
        svg.append(
            node('rect', { x: -pad, y: -pad, width: n + pad * 2, height: n + pad * 2, fill: '#fff' }),
            node('path', { d, fill: '#000' }),
        );
        return h('span', { class: 'ow-plans-qr' }, svg);
    }

    function cryptoSelection(a) {
        const assets = Array.isArray(a.supported_assets) ? a.supported_assets : [];
        const tokens = assets.find((n) => n.network === st.asset.network)?.tokens ?? [];
        const get = control('获取收款地址', chooseAsset, 'ow-primary', 'select-asset');
        if (!st.asset.token) get.disabled = true;
        return h(
            'div',
            { class: 'ow-stack ow-plans-crypto' },
            h(
                'fieldset',
                { class: 'ow-plans-options', role: 'radiogroup' },
                h('legend', { class: 'ow-muted' }, '网络'),
                assets.map((n) =>
                    radio(n.display_name || n.network, st.asset.network === n.network, 'network:' + n.network, () => {
                        st.asset.network = n.network;
                        if (!n.tokens?.includes(st.asset.token)) st.asset.token = n.tokens?.[0] ?? '';
                    }),
                ),
            ),
            h(
                'fieldset',
                { class: 'ow-plans-options ow-plans-tokens', role: 'radiogroup' },
                h('legend', { class: 'ow-muted' }, '币种'),
                tokens.map((t) =>
                    radio(t, st.asset.token === t, 'token:' + t, () => {
                        st.asset.token = t;
                    }),
                ),
            ),
            get,
        );
    }

    function cryptoTransfer(a) {
        const address = a.receive_address;
        const status = h('p', { class: 'ow-muted', role: 'status' });
        const copy = ui.button('复制地址', async () => {
            const ok = await copyText(address);
            copy.textContent = ok ? '已复制' : '复制地址';
            status.textContent = ok ? '' : '浏览器不允许自动复制，请点一下地址全选后手动复制';
            clearTimeout(copyTimer);
            copyTimer = setTimeout(() => {
                copy.textContent = '复制地址';
                status.textContent = '';
            }, 3000);
        });
        copy.dataset.focusKey = 'copy';
        const network = (st.asset.network === a.network && st.asset.label) || String(a.network || '').toUpperCase();
        const token = a.token || '';
        return h(
            'div',
            { class: 'ow-plans-center' },
            h('strong', { class: 'ow-plans-crypto-amount' }, [a.actual_amount, token].filter(Boolean).join(' ')),
            qrCode(address, '收款地址二维码'),
            h('div', { class: 'ow-plans-address-row' }, h('code', { class: 'ow-mono ow-plans-address' }, address), copy),
            status,
            h('p', { class: 'ow-warning' }, '请通过 ' + network + ' 网络转入 ' + token + '，金额需与上方完全一致'),
        );
    }

    function doneView() {
        const name = st.payPlan || planOf(sel.plan)?.name || '套餐';
        return h(
            'section',
            { class: 'ow-card ow-plans-result' },
            h('h2', { tabindex: -1, class: 'ow-plans-success' }, '套餐已生效'),
            muted(ui, name + '已生效，路由器正在同步新的节点'),
            syncLine,
            actions(ui, ui.button('返回概览', () => ui.go('overview'), 'ow-primary'), ui.button('查看账户', () => ui.go('account'))),
        );
    }

    function closedView() {
        const s = st.session,
            a = s?.payment_action;
        const expired = s?.order_status === 'expired' || a?.state === 'expired';
        const failed = s?.order_status === 'failed' || a?.state === 'failed';
        return h(
            'section',
            { class: 'ow-card ow-plans-result' },
            h('h2', { tabindex: -1 }, expired ? '订单已超时' : failed ? '支付失败' : '订单已关闭'),
            muted(ui, failed ? '如果已经扣款，请联系客服并提供订单号' : '没有完成支付，订单已关闭'),
            s ? h('span', { class: 'ow-muted ow-mono' }, '订单号 ' + s.order_no) : null,
            actions(
                ui,
                ui.button(
                    '重新选择',
                    () => {
                        st.session = null;
                        go('pick');
                    },
                    'ow-primary',
                ),
            ),
        );
    }

    // The countdown to the payment's expiry, updated in place every second.
    function tick() {
        const el = body.querySelector('.ow-plans-clock'),
            left = remainingSeconds(st.session, Date.now());
        if (!el || left == null) return;
        el.textContent = formatRemaining(left);
        el.classList.toggle('is-urgent', left <= 60);
    }
    function clock() {
        if (st.step !== 'order' || ui.signal.aborted) {
            clearInterval(ticker);
            ticker = 0;
            return;
        }
        tick();
        ticker ||= setInterval(tick, 1000);
    }

    // --- The order's payment session ---

    // A newer session from the service: shown when it changed, polled again unless it is over.
    // rev advances with each, so an answer to an older request is dropped.
    function setSession(s, enter = false) {
        const before = JSON.stringify([st.session, st.step, st.stalled, st.halted, st.asset]);
        st.session = s;
        st.preparingSince = isPreparing(s) ? st.preparingSince ?? Date.now() : null;
        st.stalled = false;
        st.halted = '';
        rev++;
        const assets = Array.isArray(s.payment_action.supported_assets) ? s.payment_action.supported_assets : [];
        if (s.payment_action.type === 'crypto_selection' && !assets.some((n) => n.network === st.asset.network && n.tokens?.includes(st.asset.token))) {
            st.asset.network = assets[0]?.network ?? '';
            st.asset.token = assets[0]?.tokens?.[0] ?? '';
        }
        if (isTerminal(s)) {
            savePending(null);
            st.step = isPaid(s) ? 'done' : 'closed';
        } else if (enter) st.step = 'order';
        if (JSON.stringify([st.session, st.step, st.stalled, st.halted, st.asset]) !== before) draw();
        schedule();
    }
    function schedule(wait) {
        clearTimeout(timer);
        if (ui.signal.aborted || st.step !== 'order' || !st.session || st.halted) return;
        wait ??= nextPollDelay(st.session, st.preparingSince, Date.now());
        if (wait == null) {
            // Still preparing after the window: the user retries by hand.
            if (isPreparing(st.session) && !st.stalled) {
                st.stalled = true;
                draw();
            }
            return;
        }
        const mine = rev,
            no = st.session.order_no;
        timer = setTimeout(async () => {
            const r = await getPayment(no);
            if (ui.signal.aborted || mine !== rev || st.step !== 'order' || st.session?.order_no !== no) return;
            if (r.ok && isSession(r.data) && r.data.order_no === no) return setSession(r.data);
            if (r.code === ORDER_ALREADY_PAID) return go('done');
            if (r.code != null && r.code !== SESSION_RECOVERING && String(r.code).startsWith('62')) {
                // The service will not say more about this order: polling stops until the user
                // asks again.
                st.halted = paymentError(r, '支付状态刷新失败');
                return draw();
            }
            // A router or network hiccup: try again on the next round.
            schedule();
        }, wait);
    }
    function resume() {
        st.halted = '';
        draw();
        schedule(0);
    }

    // --- Actions ---

    async function toConfirm() {
        const pick = { plan: sel.plan, quantity: sel.quantity, payment: sel.payment, coupon: sel.coupon.trim() };
        const seq = ++previews;
        const revert = () => {
            // The method shown stays the one the preview priced.
            if (st.step === 'confirm' && st.confirmed) sel.payment = st.confirmed.payment;
            draw();
        };
        let r;
        try {
            r = await api(ui, 'POST', '/v1/public/order/pre', {
                body: { subscribe_id: pick.plan, quantity: pick.quantity, payment: pick.payment, coupons: pick.coupon ? [pick.coupon] : [] },
            });
        } catch (e) {
            if (seq === previews && !ui.signal.aborted) revert();
            throw e;
        }
        if (ui.signal.aborted || seq !== previews) return;
        if (!r?.ok) {
            revert();
            return fail(failure(r, '价格计算失败'));
        }
        st.preview = r.data || null;
        st.confirmed = pick;
        // One key per confirmed selection: retrying the same order never creates a second one.
        st.purchaseKey = checkoutKey();
        st.step = 'confirm';
        draw();
    }

    async function payNow() {
        const c = st.confirmed;
        if (!c) return;
        // A code typed but not applied is priced first, so the order is the one shown.
        if (sel.coupon.trim() !== c.coupon) {
            await toConfirm();
            if (st.confirmed !== c && !ui.signal.aborted) say(line, '已按新的优惠码重新计算，请确认应付金额后再支付', { tone: 'done' });
            return;
        }
        const key = st.purchaseKey;
        const r = await api(ui, 'POST', '/v2/orders/purchase', {
            body: { subscribe_id: c.plan, payment_id: c.payment, quantity: c.quantity, ...(c.coupon ? { coupons: [c.coupon] } : {}) },
            idempotencyKey: key,
        });
        if (ui.signal.aborted || st.step !== 'confirm' || key !== st.purchaseKey) return;
        const plan = planOf(c.plan);
        if (!r?.ok) {
            if (r?.code === ORDER_ALREADY_PAID) {
                st.payPlan = plan?.name || '';
                return go('done');
            }
            return fail(paymentError(r, '下单失败'));
        }
        if (!isSession(r.data)) return fail('支付服务返回的数据不完整，请稍后重试');
        st.payPlan = plan?.name || '';
        st.duration = c.quantity + ' ' + unitOf(plan);
        st.preparingSince = null;
        savePending({ order_no: r.data.order_no, plan: st.payPlan, quantity: c.quantity, unit: unitOf(plan), time: Date.now() });
        setSession(r.data, true);
    }

    async function retryPreparing() {
        const s = st.session;
        if (!s) return;
        st.preparingSince = Date.now();
        st.stalled = false;
        draw();
        const r = await getPayment(s.order_no);
        if (ui.signal.aborted || st.step !== 'order' || st.session?.order_no !== s.order_no) return;
        if (r.ok && isSession(r.data) && r.data.order_no === s.order_no) return setSession(r.data);
        st.stalled = true;
        draw();
        fail(paymentError(r, '支付状态刷新失败'));
    }

    async function chooseAsset() {
        const s = st.session,
            { network, token } = st.asset;
        if (!s || !network || !token) return;
        st.asset.label = s.payment_action.supported_assets?.find((n) => n.network === network)?.display_name ?? '';
        const r = await api(ui, 'POST', orderPath(s.order_no, 'payment/selection'), {
            body: { token: token.toUpperCase(), network: network.toLowerCase() },
            idempotencyKey: checkoutKey(),
        });
        if (ui.signal.aborted || st.step !== 'order' || st.session?.order_no !== s.order_no) return;
        if (r?.ok && isSession(r.data)) return setSession(r.data);
        fail(paymentError(r, '当前币种或网络不可用'));
    }

    function askCancel() {
        const s = st.session;
        if (!s) return;
        const content = h('div', { class: 'ow-stack' }, h('p', {}, '确定取消这个订单？已经付款的话请不要取消'));
        const d = ui.dialog('取消订单', content);
        cancelDialog = d;
        d.addEventListener('close', () => {
            if (cancelDialog === d) cancelDialog = null;
        }, { once: true });
        content.append(
            actions(
                ui,
                ui.button('继续支付', () => d.close()),
                // Its error shows in the dialog, which stays open for another try.
                ui.button('取消订单', act(async () => {
                    await cancel(s.order_no);
                    d.close();
                    // The control that opened the dialog went with the order view.
                    if (st.step !== 'order') body.querySelector('h2')?.focus();
                }, true), 'ow-danger'),
            ),
        );
    }
    async function cancel(no) {
        const r = await api(ui, 'POST', orderPath(no, 'cancel'), { idempotencyKey: checkoutKey() });
        if (ui.signal.aborted || st.session?.order_no !== no) return;
        if (!r?.ok && r?.code !== ORDER_ALREADY_PAID) throw new Error(paymentError(r, '取消失败，请稍后再试'));
        if (r.code === ORDER_ALREADY_PAID || (isSession(r.data) && isPaid(r.data))) return go('done');
        savePending(null);
        st.session = null;
        st.halted = '';
        rev++;
        clearTimeout(timer);
        go('pick');
        say(line, '订单已取消', { tone: 'done' });
    }

    // --- What the page opens with ---

    const signedOut = [list, methods].some((r) => r?.error === 'not_signed_in' || r?.error === 'not signed in');
    const problems = [
        list && !list.ok ? failure(list, '套餐加载失败') : '',
        methods && !methods.ok ? failure(methods, '支付方式加载失败') : '',
    ].filter((x, i, all) => x && all.indexOf(x) === i);
    if (signedOut) say(line, [problems[0] || '请先登录 Hyper 账户', ' · ', ui.button('前往登录', () => ui.go('account'), 'ow-link')]);
    else if (problems.length) say(line, problems.join(' · '));

    // An order opened earlier in this tab and not finished yet.
    if (pending && restored) {
        if (restored.ok && isSession(restored.data) && !isTerminal(restored.data)) {
            st.payPlan = localizedText(pending.plan, '套餐');
            st.duration = (pending.quantity + ' ' + (pending.unit || '')).trim();
            setSession(restored.data, true);
        } else if (restored.ok && isSession(restored.data) && isPaid(restored.data)) {
            // Paid while the page was away: the plan is in effect, its nodes still to sync.
            st.payPlan = localizedText(pending.plan, '套餐');
            setSession(restored.data);
        } else if (transient(restored)) {
            // Kept for the next visit: the service could not be reached to ask about it.
            say(line, ['上次未完成的订单暂时无法读取 · ', ui.button('重新读取', () => ui.refresh(), 'ow-link')]);
        } else savePending(null);
    }
    if (!drawn) draw();
    return root;
}
