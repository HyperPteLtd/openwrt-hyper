/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Parse the decrypted PPanel subscription body into homeproxy-style UCI `node`
 * options (generate_client.uc turns those into sing-box outbounds / endpoints).
 *
 * Body format (hyper-tunnel packages/app-client/src/subscribe/client.ts):
 *   - plain text or base64 of it, or a SIP008 JSON document
 *   - REMARKS=<text>
 *   - STATUS=Traffic: <used> / <total> | Expires: <date>
 *   - one share link per line
 *
 * Every protocol sing-box can dial is accepted (ported from homeproxy's
 * update_subscriptions.uc parse_uri, without its router dependencies):
 *   anytls, http(s), socks(4/4a/5), shadowsocks (SIP002 / base64 / SIP008),
 *   vmess (v2rayN JSON), vless (tls / reality / vision), trojan, hysteria,
 *   hysteria2 (hy2), tuic, wireguard (wg)
 * with tcp / ws / grpc / http (h2) / httpupgrade / quic transports.
 *
 * A link sing-box could not start with is dropped here rather than written to
 * UCI: one bad outbound makes the whole sing-box config fail.
 *
 * Pure functions, no router dependencies: unit-tested on a PC (tests/snapshot_test.uc).
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

/* Values the app uses for every AnyTLS outbound */
const ANYTLS_IDLE_CHECK = '30';
const ANYTLS_IDLE_TIMEOUT = '30';
const ANYTLS_MIN_IDLE = '5';

const QUERY_SNI = [ 'sni', 'servername', 'server_name', 'peer', 'host' ];
const QUERY_SNI_STRICT = [ 'sni', 'servername', 'server_name', 'peer' ];
const QUERY_INSECURE = [ 'skip-cert-verify', 'insecure', 'allowinsecure', 'allow-insecure', 'skip_cert_verify', 'allow_insecure' ];

const SS_METHODS = [
	'none',
	'2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305',
	'aes-128-gcm', 'aes-192-gcm', 'aes-256-gcm', 'chacha20-ietf-poly1305', 'xchacha20-ietf-poly1305',
	'aes-128-ctr', 'aes-192-ctr', 'aes-256-ctr', 'aes-128-cfb', 'aes-192-cfb', 'aes-256-cfb',
	'rc4-md5', 'chacha20-ietf', 'xchacha20'
];
const UTLS_FINGERPRINTS = [ 'chrome', 'firefox', 'edge', 'safari', '360', 'qq', 'ios', 'android', 'random', 'randomized' ];
const VMESS_SECURITY = [ 'auto', 'none', 'zero', 'aes-128-gcm', 'chacha20-poly1305', 'aes-128-ctr' ];
const TUIC_CONGESTION = [ 'cubic', 'new_reno', 'bbr' ];
const TUIC_UDP_RELAY = [ 'native', 'quic' ];
const VLESS_FLOWS = [ 'xtls-rprx-vision' ];

/* Schemes this parser knows; anything else in the body is counted as unsupported */
const SCHEMES = {
	anytls: 'anytls', http: 'http', https: 'http',
	socks: 'socks', socks4: 'socks', socks4a: 'socks', socks5: 'socks', socks5h: 'socks',
	ss: 'shadowsocks', vmess: 'vmess', vless: 'vless', trojan: 'trojan',
	hysteria: 'hysteria', hysteria2: 'hysteria2', hy2: 'hysteria2',
	tuic: 'tuic', wireguard: 'wireguard', wg: 'wireguard'
};

/* Percent-decoding as decodeURIComponent does it ('+' stays '+') */
function pctdecode(s) {
	if (s == null)
		return null;
	/* a literal '%' not followed by two hex digits stays as is (display names in legacy templates) */
	return replace(s, /%([0-9A-Fa-f]{2})/g, (m, h) => chr(hex(h)));
}

/* Query values as URLSearchParams decodes them ('+' is a space) */
function formdecode(s) {
	return (s == null) ? null : pctdecode(replace(s, /\+/g, ' '));
}

function b64decode(str) {
	if (type(str) !== 'string')
		return null;
	str = replace(replace(replace(str, /[[:space:]]/g, ''), /-/g, '+'), /_/g, '/');
	str = replace(str, /=+$/, '');
	if (!length(str) || !match(str, /^[A-Za-z0-9+\/]+$/))
		return null;
	const pad = length(str) % 4;
	if (pad === 1)
		return null;
	if (pad)
		str += substr('===', 0, 4 - pad);
	return b64dec(str);
}

function nonEmpty(v) {
	return (type(v) === 'string' && trim(v) !== '') ? trim(v) : null;
}

function looksLikeText(body) {
	return index(body, 'REMARKS=') !== -1 || index(body, 'STATUS=') !== -1 ||
		match(body, /(^|[[:space:]])[A-Za-z][A-Za-z0-9+.-]*:\/\//) != null;
}

/* The body is either the line format itself, SIP008 JSON, or base64 of the lines */
export function decodeBody(body) {
	if (type(body) !== 'string')
		return '';
	if (looksLikeText(body) || substr(trim(body), 0, 1) === '{')
		return body;

	const decoded = b64decode(body);
	return (decoded != null && (looksLikeText(decoded) || substr(trim(decoded), 0, 1) === '{')) ? decoded : body;
};

function parseQuery(q) {
	const params = {};
	for (let pair in split(q || '', '&')) {
		if (pair === '')
			continue;
		const kv = split(pair, '=', 2);
		params[lc(formdecode(kv[0]))] = (length(kv) > 1) ? formdecode(kv[1]) : '';
	}
	return params;
}

function firstParam(params, keys) {
	for (let k in keys)
		if (params[k] != null && trim(params[k]) !== '')
			return trim(params[k]);
	return null;
}

/* "present", "1" and "true" mean on; anything else off */
function paramBool(params, keys, dflt) {
	for (let k in keys)
		if (params[k] != null) {
			const v = trim(params[k]);
			return (v === '' || match(v, /^(true|1|yes)$/i) != null);
		}
	return dflt;
}

function csv(v) {
	const out = filter(map(split(v || '', ','), (a) => trim(a)), (a) => a !== '');
	return length(out) ? out : null;
}

/*
 * ucode regexes use REG_NEWLINE: ^ and $ match at line breaks, so "a.com\nx"
 * would pass a ^...$ check. Regexes also stop at NUL, which is looked up apart.
 */
function hasControl(s) {
	return index(s, chr(0)) !== -1 || match(s, /[[:cntrl:]]/) != null;
}

export function isValidHost(host) {
	if (type(host) !== 'string' || !length(host) || length(host) > 253 || hasControl(host))
		return false;
	if (match(host, /^(25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])(\.(25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])){3}$/))
		return true;
	if (index(host, ':') !== -1)
		return match(host, /^[0-9A-Fa-f:.]+$/) != null && length(host) <= 45;
	return match(host, /^([A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?$/) != null;
};

export function isValidPort(port) {
	const p = int(port);
	return !hasControl(`${port}`) && match(`${port}`, /^[0-9]+$/) != null && p >= 1 && p <= 65535;
};

/*
 * "<userinfo>@<host>:<port>/<path>?<query>#<name>" (everything after "scheme://")
 * -> { userinfo, host, port, path, params, name }; userinfo / port stay raw.
 */
function splitLink(rest) {
	const out = { userinfo: null, host: null, port: null, path: '', params: {}, name: null };

	const hash = index(rest, '#');
	if (hash !== -1) {
		out.name = nonEmpty(pctdecode(substr(rest, hash + 1)));
		rest = substr(rest, 0, hash);
	}
	const qm = index(rest, '?');
	if (qm !== -1) {
		out.params = parseQuery(substr(rest, qm + 1));
		rest = substr(rest, 0, qm);
	}
	/* userinfo may hold standard base64 ('/'), so cut it before looking for a path */
	const at = rindex(rest, '@');
	if (at !== -1) {
		out.userinfo = substr(rest, 0, at);
		rest = substr(rest, at + 1);
	}
	const slash = index(rest, '/');
	if (slash !== -1) {
		out.path = substr(rest, slash);
		rest = substr(rest, 0, slash);
	}

	if (rest === '' || match(rest, /[[:space:][:cntrl:]\\]/))
		return null;

	const v6 = match(rest, /^\[([0-9A-Fa-f:.]+)\](:(.*))?$/);
	if (v6) {
		out.host = v6[1];
		out.port = v6[3];
	} else {
		const hp = match(rest, /^([^]:[]+)(:(.*))?$/);
		if (!hp)
			return null;
		out.host = hp[1];
		out.port = hp[3];
	}
	if (out.port === '')
		out.port = null;
	return out;
}

/* "user:pass" -> [ user, pass ] (raw, still percent-encoded) */
function splitUserinfo(ui) {
	if (ui == null)
		return [ null, null ];
	const c = index(ui, ':');
	return (c === -1) ? [ ui, null ] : [ substr(ui, 0, c), substr(ui, c + 1) ];
}

function hasFeature(opts, tag) {
	/* no feature list (sing-box version not readable): assume a full build */
	return (type(opts?.features) !== 'object') || !!opts.features[tag];
}

function unsupported(reason) {
	return { unsupported: reason };
}

/* sing-box TLS options shared by vless / trojan / vmess / http */
function applyTls(node, p, security) {
	node.tls = '1';
	node.tls_sni = firstParam(p, QUERY_SNI_STRICT);
	node.tls_alpn = csv(p.alpn);
	node.tls_insecure = paramBool(p, QUERY_INSECURE, false) ? '1' : '0';
	const fp = lc(p.fp || '');
	node.tls_utls = (fp in UTLS_FINGERPRINTS) ? fp : null;

	if (security === 'reality') {
		const pbk = nonEmpty(p.pbk || p.publickey);
		if (!pbk || !match(pbk, /^[A-Za-z0-9_=+\/-]{40,46}$/) || index(pbk, '\\') !== -1)
			return false;
		node.tls_reality = '1';
		node.tls_reality_public_key = pbk;
		const sid = nonEmpty(p.sid || p.shortid);
		if (sid && !match(sid, /^[0-9A-Fa-f]{0,16}$/))
			return false;
		node.tls_reality_short_id = sid;
		/* sing-box refuses REALITY without uTLS */
		node.tls_utls = node.tls_utls || 'chrome';
		node.tls_insecure = null;
	}
	return true;
}

function wsPath(node, path) {
	node.ws_path = path || '/';
	/* Xray style early data: /path?ed=2048 */
	const ed = match(node.ws_path, /^(.*)\?ed=([0-9]+)$/);
	if (ed) {
		node.ws_path = ed[1] || '/';
		node.websocket_early_data = ed[2];
		node.websocket_early_data_header = 'Sec-WebSocket-Protocol';
	}
}

/* V2Ray transports by name; returns an unsupported() marker or null when fine */
function applyTransport(node, net, host, path, extra, opts) {
	net = lc(net || 'tcp');
	switch (net) {
	case 'tcp':
	case 'raw':
	case 'none':
	case '':
		/* TCP with HTTP header obfuscation has no sing-box equivalent */
		if (lc(extra.header_type || 'none') !== 'none')
			return unsupported('tcp/http header');
		return null;
	case 'ws':
	case 'websocket':
		node.transport = 'ws';
		node.ws_host = nonEmpty(host);
		wsPath(node, nonEmpty(path));
		return null;
	case 'grpc':
	case 'gun':
		node.transport = 'grpc';
		node.grpc_servicename = nonEmpty(extra.service_name) ?? nonEmpty(path);
		return null;
	case 'http':
	case 'h2':
		node.transport = 'http';
		node.http_host = csv(host);
		node.http_path = nonEmpty(path);
		return null;
	case 'httpupgrade':
		node.transport = 'httpupgrade';
		node.httpupgrade_host = nonEmpty(host);
		node.http_path = nonEmpty(path);
		return null;
	case 'quic':
		if (!hasFeature(opts, 'with_quic'))
			return unsupported('quic transport (sing-box built without QUIC)');
		if (lc(extra.quic_security || 'none') !== 'none')
			return unsupported('quic security');
		node.transport = 'quic';
		return null;
	default:
		/* kcp, xhttp / splithttp, ... */
		return unsupported(`${net} transport`);
	}
}

/*
 * anytls://<password>@<server>:<port>[/][?query][#name] -> node options, or null.
 * `opts.country` tags the node; `opts.insecure_default` applies when the link has
 * no insecure flag (the app defaults to skipping certificate checks).
 */
export function parseAnyTls(line, opts) {
	opts = opts || {};
	if (type(line) !== 'string' || substr(line, 0, 9) !== 'anytls://')
		return null;

	const l = splitLink(substr(line, 9));
	if (!l)
		return null;
	const password = pctdecode(l.userinfo);
	const port = l.port || '443';
	if (!nonEmpty(password) || !isValidHost(l.host) || !isValidPort(port))
		return null;

	const p = l.params;
	return {
		label: l.name || l.host,
		type: 'anytls',
		address: l.host,
		port: `${int(port)}`,
		password,
		tls: '1',
		tls_sni: firstParam(p, QUERY_SNI) || l.host,
		tls_insecure: paramBool(p, QUERY_INSECURE, opts.insecure_default ?? true) ? '1' : '0',
		tls_alpn: csv(p.alpn),
		anytls_idle_session_check_interval: ANYTLS_IDLE_CHECK,
		anytls_idle_session_timeout: ANYTLS_IDLE_TIMEOUT,
		anytls_min_idle_session: ANYTLS_MIN_IDLE,
		country: opts.country || null,
		grouphash: opts.group || 'ppanel'
	};
};

function parseHttp(scheme, l) {
	const ui = splitUserinfo(l.userinfo);
	const node = {
		type: 'http',
		address: l.host,
		port: l.port || ((scheme === 'https') ? '443' : '80'),
		username: nonEmpty(pctdecode(ui[0])),
		password: pctdecode(ui[1])
	};
	if (scheme === 'https')
		applyTls(node, l.params, 'tls');
	return node;
}

function parseSocks(scheme, l) {
	let ui = splitUserinfo(l.userinfo);
	/* v2rayN: socks://base64(user:pass)@host:port */
	if (ui[0] != null && ui[1] == null) {
		const d = b64decode(pctdecode(ui[0]));
		if (d != null && index(d, ':') !== -1)
			ui = map(splitUserinfo(d), (x) => (x == null) ? null : replace(x, /%/g, '%25'));
	}
	return {
		type: 'socks',
		address: l.host,
		port: l.port || '1080',
		username: nonEmpty(pctdecode(ui[0])),
		password: pctdecode(ui[1]),
		socks_version: (scheme === 'socks4a') ? '4a' : ((scheme === 'socks4') ? '4' : '5')
	};
}

function ssNode(method, password, host, port, plugin, label) {
	method = lc(trim(method || ''));
	if (!(method in SS_METHODS))
		return unsupported(`shadowsocks method ${method || '?'}`);
	if (method !== 'none' && !nonEmpty(password))
		return null;
	/* 2022 ciphers take base64 keys of a fixed size ("ipsk:upsk" for multi-user); sing-box refuses others */
	if (substr(method, 0, 12) === '2022-blake3-') {
		const size = (method === '2022-blake3-aes-128-gcm') ? 16 : 32;
		for (let k in split(password, ':')) {
			const raw = b64decode(k);
			if (raw == null || length(raw) !== size)
				return null;
		}
	}

	let ss_plugin = null, ss_plugin_opts = null;
	if (nonEmpty(plugin)) {
		const info = split(plugin, ';', 2);
		ss_plugin = trim(info[0]);
		if (ss_plugin === 'simple-obfs')
			ss_plugin = 'obfs-local';
		if (!(ss_plugin in [ 'obfs-local', 'v2ray-plugin' ]))
			return unsupported(`shadowsocks plugin ${ss_plugin}`);
		ss_plugin_opts = nonEmpty(info[1]);
	}

	return {
		label,
		type: 'shadowsocks',
		address: host,
		port: `${port}`,
		shadowsocks_encrypt_method: method,
		password,
		shadowsocks_plugin: ss_plugin,
		shadowsocks_plugin_opts: ss_plugin_opts
	};
}

/* re-encode decoded text so splitLink reads it back unchanged */
function esc(s) {
	return replace(s, /[%#?\/@]/g, (c) => sprintf('%%%02X', ord(c)));
}

function parseShadowsocks(rest) {
	/* whole-link base64: ss://base64(method:password@host:port)[?plugin=..]#name */
	const hash = index(rest, '#');
	let head = (hash === -1) ? rest : substr(rest, 0, hash);
	if (index(head, '@') === -1) {
		const qm = index(head, '?');
		const query = (qm === -1) ? '' : substr(head, qm);
		if (qm !== -1)
			head = substr(head, 0, qm);
		const d = b64decode(pctdecode(replace(head, /\/$/, '')));
		if (d == null || index(d, '@') === -1)
			return null;
		const at = rindex(d, '@');
		rest = esc(substr(d, 0, at)) + substr(d, at) + query + ((hash === -1) ? '' : substr(rest, hash));
	}

	const l = splitLink(rest);
	if (!l || l.userinfo == null)
		return null;

	/* SIP002: userinfo is "method:password" percent-encoded, or base64 of it */
	let ui = splitUserinfo(l.userinfo);
	let method, password;
	if (ui[1] != null) {
		method = pctdecode(ui[0]);
		password = pctdecode(ui[1]);
	} else {
		const d = b64decode(pctdecode(ui[0]));
		if (d == null || index(d, ':') === -1)
			return null;
		const c = index(d, ':');
		method = substr(d, 0, c);
		password = substr(d, c + 1);
	}

	return ssNode(method, password, l.host, l.port, l.params.plugin, l.name);
}

function parseVmess(rest, opts) {
	const hash = index(rest, '#');
	const head = (hash === -1) ? rest : substr(rest, 0, hash);
	/* Shadowrocket "vmess://base64?remarks=..." is a different format */
	if (index(head, '?') !== -1)
		return unsupported('vmess shadowrocket format');

	let v;
	try {
		v = json(b64decode(head) || '');
	} catch (e) {
		return null;
	}
	if (type(v) !== 'object')
		return null;
	if (v.v != null && `${v.v}` !== '2')
		return unsupported(`vmess link version ${v.v}`);

	const uuid = nonEmpty(`${v.id ?? ''}`);
	if (!uuid)
		return null;

	const scy = lc(nonEmpty(`${v.scy ?? ''}`) || 'auto');
	if (!(scy in VMESS_SECURITY))
		return unsupported(`vmess security ${scy}`);

	const node = {
		label: nonEmpty(`${v.ps ?? ''}`),
		type: 'vmess',
		address: nonEmpty(`${v.add ?? ''}`),
		port: nonEmpty(`${v.port ?? ''}`),
		uuid,
		vmess_alterid: match(`${v.aid ?? '0'}`, /^[0-9]+$/) ? `${int(v.aid ?? 0)}` : '0',
		vmess_encrypt: scy,
		vmess_global_padding: '1'
	};

	let net = lc(`${v.net ?? 'tcp'}`);
	const header = lc(`${v.type ?? 'none'}`);
	const bad = applyTransport(node, net, `${v.host ?? ''}`, `${v.path ?? ''}`, {
		header_type: (net in [ 'tcp', '' ]) ? header : 'none',
		quic_security: (net === 'quic') ? header : 'none',
		service_name: (net === 'grpc') ? `${v.path ?? ''}` : null
	}, opts);
	if (bad)
		return bad;
	if (net === 'quic' && nonEmpty(`${v.path ?? ''}`))
		return unsupported('quic key');

	if (lc(`${v.tls ?? ''}`) === 'tls') {
		applyTls(node, {
			sni: nonEmpty(`${v.sni ?? ''}`) || nonEmpty(`${v.host ?? ''}`),
			alpn: `${v.alpn ?? ''}`,
			fp: `${v.fp ?? ''}`,
			allowinsecure: (v.allowInsecure != null) ? ((v.allowInsecure in [ true, 1, '1', 'true' ]) ? '1' : '0') : null,
			insecure: (v.skip_cert_verify != null) ? ((v.skip_cert_verify in [ true, 1, '1', 'true' ]) ? '1' : '0') : null
		}, 'tls');
	}
	return node;
}

function parseVless(l, opts) {
	const p = l.params;
	const uuid = nonEmpty(pctdecode(l.userinfo));
	if (!uuid)
		return null;

	const security = lc(p.security || 'none');
	const node = { label: l.name, type: 'vless', address: l.host, port: l.port || '443', uuid };

	const bad = applyTransport(node, p.type, p.host, p.path, {
		header_type: p.headertype, quic_security: p.quicsecurity, service_name: p.servicename
	}, opts);
	if (bad)
		return bad;

	if (security in [ 'tls', 'reality' ]) {
		if (!applyTls(node, p, security))
			return null;
		const flow = nonEmpty(p.flow);
		if (flow) {
			if (!(flow in VLESS_FLOWS))
				return unsupported(`vless flow ${flow}`);
			node.vless_flow = flow;
		}
	} else if (security === 'xtls')
		return unsupported('xtls');
	else if (security !== 'none')
		return unsupported(`vless security ${security}`);

	node.packet_encoding = (p.packetencoding in [ 'xudp', 'packetaddr' ]) ? p.packetencoding : null;
	return node;
}

function parseTrojan(l, opts) {
	const p = l.params;
	const password = pctdecode(l.userinfo);
	if (!nonEmpty(password))
		return null;

	const node = { label: l.name, type: 'trojan', address: l.host, port: l.port || '443', password };
	const bad = applyTransport(node, p.type, p.host, p.path, {
		header_type: p.headertype, quic_security: p.quicsecurity, service_name: p.servicename
	}, opts);
	if (bad)
		return bad;

	const security = lc(p.security || 'tls');
	if (security in [ 'tls', 'reality' ]) {
		if (!applyTls(node, p, security))
			return null;
	} else if (security !== 'none')
		return unsupported(`trojan security ${security}`);
	return node;
}

function parseHysteria(l, opts) {
	const p = l.params;
	if (!hasFeature(opts, 'with_quic'))
		return unsupported('hysteria (sing-box built without QUIC)');
	if (p.protocol && p.protocol !== 'udp')
		return unsupported(`hysteria protocol ${p.protocol}`);
	/* sing-box needs both speeds for Hysteria 1 */
	const up = match(p.upmbps || p.up || '', /^[0-9]+/), down = match(p.downmbps || p.down || '', /^[0-9]+/);
	if (!up || !down)
		return null;

	const auth = nonEmpty(p.auth);
	return {
		label: l.name,
		type: 'hysteria',
		address: l.host,
		port: l.port,
		hysteria_protocol: 'udp',
		hysteria_auth_type: auth ? 'string' : null,
		hysteria_auth_payload: auth,
		hysteria_obfs_password: nonEmpty(p.obfsparam),
		hysteria_up_mbps: up[0],
		hysteria_down_mbps: down[0],
		tls: '1',
		tls_sni: firstParam(p, QUERY_SNI_STRICT),
		tls_insecure: paramBool(p, QUERY_INSECURE, false) ? '1' : '0',
		tls_alpn: csv(p.alpn)
	};
}

function parseHysteria2(l, opts) {
	const p = l.params;
	if (!hasFeature(opts, 'with_quic'))
		return unsupported('hysteria2 (sing-box built without QUIC)');

	/* "443", or a hopping list such as "443,20000-30000": dial the first port */
	let port = l.port || '443';
	if (!match(port, /^[0-9]+$/)) {
		const first = match(port, /^([0-9]+)[-,]/);
		if (!first)
			return null;
		port = first[1];
	}

	const obfs = lc(p.obfs || '');
	if (obfs !== '' && obfs !== 'none' && obfs !== 'salamander')
		return unsupported(`hysteria2 obfs ${obfs}`);
	const up = match(p.upmbps || p.up || '', /^[0-9]+/), down = match(p.downmbps || p.down || '', /^[0-9]+/);

	return {
		label: l.name,
		type: 'hysteria2',
		address: l.host,
		port,
		password: pctdecode(l.userinfo),
		hysteria_obfs_type: (obfs === 'salamander') ? 'salamander' : null,
		hysteria_obfs_password: (obfs === 'salamander') ? nonEmpty(p['obfs-password']) : null,
		hysteria_up_mbps: up ? up[0] : null,
		hysteria_down_mbps: down ? down[0] : null,
		tls: '1',
		tls_sni: firstParam(p, QUERY_SNI_STRICT),
		tls_insecure: paramBool(p, QUERY_INSECURE, false) ? '1' : '0',
		tls_alpn: csv(p.alpn)
	};
}

function parseTuic(l, opts) {
	const p = l.params;
	if (!hasFeature(opts, 'with_quic'))
		return unsupported('tuic (sing-box built without QUIC)');

	const ui = splitUserinfo(l.userinfo);
	const uuid = nonEmpty(pctdecode(ui[0])), password = pctdecode(ui[1]);
	if (!uuid || !nonEmpty(password))
		return null;

	const cc = lc(p.congestion_control || p.congestion || '');
	const relay = lc(p.udp_relay_mode || '');
	return {
		label: l.name,
		type: 'tuic',
		address: l.host,
		port: l.port,
		uuid,
		password,
		tuic_congestion_control: (cc in TUIC_CONGESTION) ? cc : null,
		tuic_udp_relay_mode: (relay in TUIC_UDP_RELAY) ? relay : null,
		tls: '1',
		tls_sni: firstParam(p, QUERY_SNI_STRICT),
		tls_insecure: paramBool(p, QUERY_INSECURE, false) ? '1' : '0',
		tls_alpn: csv(p.alpn)
	};
}

function wgKey(k) {
	/* an unencoded '+' in the query reads back as a space */
	k = nonEmpty((type(k) === 'string') ? replace(k, / /g, '+') : k);
	return (k && match(k, /^[A-Za-z0-9+\/]{42,43}=?$/)) ? k : null;
}

function parseWireguard(l, opts) {
	const p = l.params;
	if (!hasFeature(opts, 'with_wireguard'))
		return unsupported('wireguard (sing-box built without WireGuard)');

	const private_key = wgKey(pctdecode(l.userinfo) || p.privatekey || p.private_key);
	const public_key = wgKey(p.publickey || p.public_key || p.peer_public_key || p.peerpublickey);
	if (!private_key || !public_key)
		return null;

	const addresses = [];
	for (let a in csv(p.address || p.ip || p.local_address || p.localaddress) || []) {
		if (match(a, /^[0-9.]+(\/[0-9]{1,2})?$/))
			push(addresses, (index(a, '/') === -1) ? `${a}/32` : a);
		else if (match(a, /^[0-9A-Fa-f:]+(\/[0-9]{1,3})?$/))
			push(addresses, (index(a, '/') === -1) ? `${a}/128` : a);
		else
			return null;
	}
	if (!length(addresses))
		return null;

	let reserved = null;
	if (nonEmpty(p.reserved)) {
		reserved = csv(p.reserved);
		if (length(reserved) !== 3 || length(filter(reserved, (r) => match(r, /^[0-9]+$/) && int(r) <= 255)) !== 3)
			return null;
	}
	const mtu = match(p.mtu || '', /^[0-9]+$/) ? p.mtu : null;
	const keepalive = match(p.keepalive || p.persistent_keepalive || '', /^[0-9]+$/) ? (p.keepalive || p.persistent_keepalive) : null;

	return {
		label: l.name,
		type: 'wireguard',
		address: l.host,
		port: l.port || '51820',
		wireguard_private_key: private_key,
		wireguard_peer_public_key: public_key,
		wireguard_pre_shared_key: wgKey(p.presharedkey || p.pre_shared_key || p.psk),
		wireguard_local_address: addresses,
		wireguard_reserved: reserved,
		wireguard_mtu: mtu,
		wireguard_persistent_keepalive_interval: keepalive
	};
}

/* Shared checks: valid host / port, label fallback, tags; drops null options */
function finish(node, opts) {
	if (type(node) !== 'object' || node.unsupported)
		return node;

	node.address = replace(`${node.address ?? ''}`, /[][]/g, '');
	node.port = (node.port == null) ? null : `${node.port}`;
	if (!isValidHost(node.address) || !isValidPort(node.port))
		return null;
	node.port = `${int(node.port)}`;

	/*
	 * Every option ends up in UCI and the sing-box config: a control character
	 * in a credential, SNI, path... means a broken or hostile link. The label
	 * is free text, so it is only cleaned.
	 */
	if (type(node.label) === 'string')
		node.label = trim(replace(node.label, /[[:cntrl:]]+/g, ' '));
	for (let k, v in node) {
		if (k === 'label')
			continue;
		for (let x in ((type(v) === 'array') ? v : [ v ]))
			if (type(x) === 'string' && hasControl(x))
				return null;
	}

	if (!nonEmpty(node.label))
		node.label = ((index(node.address, ':') !== -1) ? `[${node.address}]` : node.address) + ':' + node.port;
	node.country = opts.country || null;
	node.grouphash = opts.group || 'ppanel';

	for (let k in keys(node))
		if (node[k] == null)
			delete node[k];
	return node;
}

/*
 * One share link -> node options, `{ unsupported: reason }` for a link sing-box
 * (or this build of it) cannot use, or null for a broken link.
 *   opts: { country, group, insecure_default (AnyTLS only), features (sing-box build tags) }
 */
export function parseNode(line, opts) {
	opts = opts || {};
	if (type(line) !== 'string')
		return null;
	line = trim(line);

	const m = match(line, /^([A-Za-z][A-Za-z0-9+.-]*):\/\/(.*)$/);
	if (!m)
		return null;
	const scheme = lc(m[1]), rest = m[2];
	if (!SCHEMES[scheme])
		return unsupported(`${scheme}://`);

	if (scheme === 'anytls')
		return parseAnyTls('anytls://' + rest, opts);
	if (scheme === 'ss')
		return finish(parseShadowsocks(rest), opts);
	if (scheme === 'vmess')
		return finish(parseVmess(rest, opts), opts);

	const l = splitLink(rest);
	if (!l)
		return null;

	let node;
	switch (SCHEMES[scheme]) {
	case 'http': node = parseHttp(scheme, l); break;
	case 'socks': node = parseSocks(scheme, l); break;
	case 'vless': node = parseVless(l, opts); break;
	case 'trojan': node = parseTrojan(l, opts); break;
	case 'hysteria': node = parseHysteria(l, opts); break;
	case 'hysteria2': node = parseHysteria2(l, opts); break;
	case 'tuic': node = parseTuic(l, opts); break;
	case 'wireguard': node = parseWireguard(l, opts); break;
	}
	if (type(node) === 'object' && !node.unsupported && node.label == null)
		node.label = l.name;
	return finish(node, opts);
};

/* SIP008 JSON (https://shadowsocks.org/doc/sip008.html) -> node list */
function parseSip008(doc, opts, result) {
	for (let s in (type(doc?.servers) === 'array') ? doc.servers : []) {
		const n = finish(ssNode(s?.method, `${s?.password ?? ''}`, `${s?.server ?? ''}`, `${s?.server_port ?? ''}`,
			s?.plugin ? (s.plugin + (s.plugin_opts ? ';' + s.plugin_opts : '')) : null, nonEmpty(`${s?.remarks ?? ''}`)), opts);
		if (n?.unsupported)
			push(result.unsupported, n.unsupported);
		else if (n)
			push(result.nodes, n);
		else
			result.invalid++;
	}
}

/* "Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09 12:00:00 +0800" -> { traffic_used, traffic_total, expires } */
export function parseStatus(status) {
	const out = {};
	for (let part in split(status || '', '|')) {
		part = trim(part);
		if (substr(part, 0, 8) === 'Traffic:') {
			const t = split(trim(substr(part, 8)), '/');
			if (length(t) === 2) {
				out.traffic_used = trim(t[0]);
				out.traffic_total = trim(t[1]);
			}
		} else if (substr(part, 0, 8) === 'Expires:')
			out.expires = trim(substr(part, 8));
	}
	return out;
};

/* Whole decrypted body -> { nodes: [...], remarks, status, invalid, unsupported: [reason...] } */
export function parseSubscription(body, opts) {
	opts = opts || {};
	const text = decodeBody(body);
	const result = { nodes: [], remarks: null, status: null, invalid: 0, unsupported: [] };

	if (substr(trim(text), 0, 1) === '{') {
		let doc;
		try {
			doc = json(text);
		} catch (e) {
			doc = null;
		}
		if (type(doc?.servers) === 'array') {
			parseSip008(doc, opts, result);
			return result;
		}
	}

	for (let line in split(replace(text, /\r/g, ''), '\n')) {
		line = trim(line);
		if (line === '')
			continue;
		if (substr(line, 0, 8) === 'REMARKS=') {
			result.remarks = trim(substr(line, 8));
			continue;
		}
		if (substr(line, 0, 7) === 'STATUS=') {
			result.status = trim(substr(line, 7));
			continue;
		}
		if (!match(line, /^[A-Za-z][A-Za-z0-9+.-]*:\/\//))
			continue;

		const node = parseNode(line, opts);
		if (node?.unsupported)
			push(result.unsupported, node.unsupported);
		else if (node)
			push(result.nodes, node);
		else
			result.invalid++;
	}

	return result;
};

/*
 * `sing-box version` output -> { with_quic: true, ... } from its "Tags:" line,
 * or null when there is none (then every protocol is assumed to be available).
 */
export function parseFeatures(output) {
	for (let line in split(output || '', '\n')) {
		line = trim(line);
		if (substr(line, 0, 5) !== 'Tags:')
			continue;
		const features = {};
		for (let t in split(substr(line, 5), ','))
			if (trim(t) !== '')
				features[trim(t)] = true;
		return features;
	}
	return null;
};

/* /api/subscribe/location -> [{ code, sort }], highest sort first */
export function parseLocations(raw) {
	const data = (type(raw?.data) === 'object') ? raw.data : raw;
	const list = [];
	for (let code in (type(data?.locations) === 'object') ? data.locations : {}) {
		const c = trim(code);
		if (c === '' || !match(c, /^[A-Za-z0-9_-]{1,16}$/))
			continue;
		const s = data.locations[code]?.sort;
		push(list, { code: c, sort: (type(s) in ['int', 'double']) ? s : (match(`${s}`, /^-?[0-9]+$/) ? int(s) : -1000000) });
	}
	return sort(list, (a, b) => (b.sort - a.sort) || ((a.code < b.code) ? -1 : 1));
};
