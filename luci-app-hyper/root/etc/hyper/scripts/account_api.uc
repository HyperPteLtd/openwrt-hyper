/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Hyper service API client for luci-app-hyper.
 *
 * The router signs in as a *device* (Login-Type: device + a stable identifier),
 * so it takes one device slot on the account and is allowed to receive the
 * subscription token. The JWT and the subscription token live only in
 * /etc/hyper/session (0600); the browser never sees them.
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

import { cursor } from 'uci';
import { isEmpty, matchWhole, readSession, writeSession } from 'hyper';
import { currentApiBase, requestApi } from 'api_endpoints';

const UCI_CONFIG = 'hyper';
const USER_AGENT = 'luci-app-hyper (OpenWrt)';

/* Paths the browser may reach through rpcd `api`. Everything else is refused. */
const API_ALLOWLIST = [
	/^\/v1\/auth\/(check|login|register|reset)$/,
	/^\/v1\/common\/(site\/config|site\/tos|site\/privacy|send_code|check_verification_code|heartbeat|exchange-rate)$/,
	/^\/v1\/public\/(announcement|document|order|payment|portal|subscribe|ticket|coupon)\//,
	/^\/v1\/public\/user\/(info|subscribe|devices|device\/kick_offline|password|profile|notify|affiliate\/count|affiliate\/list|balance_log|commission_log|login_log|logout)$/,
	/^\/v1\/public\/app\/(check|latest)$/,
	/^\/v2\/subscribe\/locations$/,
	/* payment-v2, as the Hyper app pays (no Apple IAP on a router) */
	/^\/v2\/orders\/purchase$/,
	/^\/v2\/orders\/[A-Za-z0-9_.-]{1,64}\/(payment|payment\/selection|cancel)$/
];

/* Idempotency-Key for payment-v2 writes, same shape the app checks */
export function isValidIdempotencyKey(key) {
	return matchWhole(key, /^[A-Za-z0-9._:-]{8,128}$/);
};

export function config() {
	const uci = cursor();
	uci.load(UCI_CONFIG);

	return {
		api_base: currentApiBase(),
		device_id: uci.get(UCI_CONFIG, 'account', 'device_id') || ''
	};
};

export function isAllowedPath(path) {
	/* POSIX regex: shorthand classes like \s are not special inside [...] */
	if (type(path) !== 'string' || match(path, /\.\.|[[:space:][:cntrl:]?#]/))
		return false;

	for (let re in API_ALLOWLIST)
		if (match(path, re))
			return true;

	return false;
};

function encodeQuery(query) {
	if (type(query) !== 'object' || !length(query))
		return '';

	const parts = [];
	for (let k in query)
		if (query[k] != null)
			push(parts, `${k}=${replace(`${query[k]}`, /[^A-Za-z0-9_.~-]/g, (c) => sprintf('%%%02X', ord(c)))}`);

	return length(parts) ? ('?' + join('&', parts)) : '';
}

/*
 * Call Hyper service. opts: { query, body, auth (default true), timeout,
 *   raw (return the body unparsed), proxy,
 *   idempotency_key (payment-v2 writes) }.
 * Returns { ok, status, code, msg, data, error } or, with raw, { ok, status, body, error }.
 */
export function request(method, path, opts) {
	opts = opts || {};
	const headers = {
		'Accept': 'application/json',
		'Content-Type': (opts.body != null) ? 'application/json' : null,
		'User-Agent': USER_AGENT,
		'Login-Type': 'device',
		'X-Device-Platform': 'Linux',
		'Idempotency-Key': isValidIdempotencyKey(opts.idempotency_key) ? opts.idempotency_key : null
	};

	if (opts.auth !== false) {
		const session = readSession();
		if (session?.jwt)
			headers['Authorization'] = 'Bearer ' + session.jwt;
	}
	/* Only reads and payment-v2 writes carrying an idempotency key may be replayed. */
	const replayable = method === 'GET' || method === 'HEAD'
		|| (method === 'POST' && index(path, '/v2/orders/') === 0 && isValidIdempotencyKey(opts.idempotency_key));
	const res = requestApi(method, path + encodeQuery(opts.query), headers, opts.body, opts.timeout, opts.proxy, replayable);
	if (res.error)
		return { ok: false, status: res.status || 0, error: res.error };

	if (opts.raw)
		return { ok: res.status >= 200 && res.status < 300, status: res.status, body: res.body };

	let payload;
	try {
		payload = json(res.body || 'null');
	} catch (e) {
		return { ok: false, status: res.status, error: 'invalid_json', raw: substr(res.body, 0, 512) };
	}

	/* Wrapped responses carry { code, msg, data }; raw ones (e.g. /v2/subscribe/*) do not. */
	const wrapped = (type(payload) === 'object' && 'code' in payload && 'msg' in payload);
	const code = wrapped ? payload.code : null;
	const ok = (res.status >= 200 && res.status < 300) && (!wrapped || code === 200 || code === 0);

	return {
		ok,
		status: res.status,
		code,
		msg: wrapped ? payload.msg : null,
		data: wrapped ? payload.data : payload
	};
};

/*
 * Email login as a device, same request as the Hyper app: the verification code
 * doubles as the password and an unknown email is registered on the fly.
 * Stores the JWT, returns { ok, error }.
 */
export function login(email, password, code, cf_token, invite) {
	const cfg = config();
	const res = request('POST', '/v1/auth/login', {
		auth: false,
		body: {
			identifier: cfg.device_id || email,
			email,
			code: code || null,
			password: password || code || null,
			cf_token: cf_token || null,
			invite: invite || null
		}
	});

	const jwt = res.data?.token;
	if (!res.ok || isEmpty(jwt))
		return { ok: false, code: res.code, msg: res.msg, error: res.error || 'login_failed' };

	writeSession({ jwt, email, login_at: time() });
	return { ok: true };
};

export function register(email, password, code, invite, cf_token) {
	const cfg = config();
	const res = request('POST', '/v1/auth/register', {
		auth: false,
		body: {
			email, password,
			code: code || null,
			invite: invite || null,
			cf_token: cf_token || null,
			identifier: cfg.device_id
		}
	});

	const jwt = res.data?.token;
	if (!res.ok || isEmpty(jwt))
		return { ok: false, code: res.code, msg: res.msg, error: res.error || 'register_failed' };

	writeSession({ jwt, email, login_at: time() });
	return { ok: true };
};

/*
 * Pick the subscription the router should use and remember its delivery token.
 * Only device sessions receive `token`, which is why login uses Login-Type: device.
 */
export function refreshSubscriptionToken(proxy) {
	const identity = readSession()?.jwt;
	const res = request('GET', '/v1/public/user/subscribe', { proxy });
	if (!res.ok)
		return { ok: false, status: res.status, code: res.code, msg: res.msg, error: res.error || 'subscribe_list_failed' };
	if (!identity || readSession()?.jwt !== identity)
		return { ok: false, status: 409, error: 'session_changed' };

	const list = res.data?.list || [];
	let picked = null;
	for (let s in list) {
		if (isEmpty(s.token))
			continue;
		/* Prefer an active subscription (status 1); fall back to the first one with a token. */
		if (s.status === 1) {
			picked = s;
			break;
		}
		picked = picked || s;
	}

	if (!picked)
		return { ok: false, status: 200, error: 'no_subscription' };

	const session = readSession() || {};
	if (session.jwt !== identity)
		return { ok: false, status: 409, error: 'session_changed' };
	session.sub_token = picked.token;
	session.user_subscribe_id = picked.id;
	session.expire_time = picked.expire_time;
	writeSession(session);

	return { ok: true, subscription: { id: picked.id, expire_time: picked.expire_time, status: picked.status } };
};

/* Email verification code for login (type 2 = auth, as in the Hyper app) */
export const EMAIL_CODE_TYPE_AUTH = 2;

/* Subscription endpoints always live under the API address, as in the Hyper app */
export const SUBSCRIBE_PATH = '/api/subscribe';

export function subscribeEndpoint() {
	return { path: SUBSCRIBE_PATH };
};
