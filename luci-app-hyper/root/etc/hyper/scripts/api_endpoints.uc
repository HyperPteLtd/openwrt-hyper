/* SPDX-License-Identifier: GPL-2.0-only
 * Hyper API discovery, matching hyper-tunnel's encrypted TXT protocol.
 * Cache only public endpoint metadata in RAM; status polling never does I/O to the network.
 */
import { mkdir, mkstemp, readfile, rename, unlink, writefile } from 'fs';
import { CRYPT_BIN, RUN_DIR, executeCommand, hasControlChar, httpRequest, matchWhole } from 'hyper';

export const DEFAULT_API_BASE = 'https://api.cloudcdnhyper.com';
export const STATIC_API_BASE = 'https://api.hypervpn.app';
export const TXT_HOST = 'tt.cloudcdnhyper.com';
const CACHE_FILE = RUN_DIR + '/api-endpoints.json';
const TXT_TTL = 1800, TXT_RETRY = 300, HEALTH_TTL = 60;
const DNS_SERVERS = [ 'https://dns.alidns.com/resolve', 'https://doh.pub/resolve', 'https://dns.google/resolve' ];

/* TXT supplies HTTPS origins only, never credentials, ports, paths or query strings. */
export function normalizeApiOrigin(value) {
	if (type(value) !== 'string' || hasControlChar(value)) return null;
	let host = lc(trim(value));
	if (index(host, 'https://') === 0) host = substr(host, 8);
	host = replace(host, /\/$/, '');
	if (length(host) > 253 || index(host, '.') < 0) return null;
	for (let label in split(host, '.'))
		if (!matchWhole(label, /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/) || length(label) > 63)
			return null;
	return 'https://' + host;
};

function origins(values) {
	const result = [];
	for (let value in type(values) === 'array' ? values : []) {
		const base = normalizeApiOrigin(value);
		if (base && !(base in result)) push(result, base);
		if (length(result) >= 16) break;
	}
	return result;
}

export function parseTxtOrigins(plain) {
	if (type(plain) !== 'string' || index(plain, 'v=1|') !== 0) return [];
	return origins(split(replace(substr(plain, 4), /\\010/g, '\n'), /[[:space:],;|]+/));
};

function readCache() {
	try { return json(readfile(CACHE_FILE) || 'null'); }
	catch (e) { return null; }
}

function saveCache(cache) {
	mkdir(RUN_DIR);
	/* OpenWrt 23.05/24.10 have no fs.mkdtemp; BusyBox mktemp is available. */
	const tmp = executeCommand('mktemp', CACHE_FILE + '.XXXXXX');
	if (tmp.exitcode !== 0) return;
	const path = trim(tmp.stdout || '');
	if (index(path, CACHE_FILE + '.') !== 0) return;
	if (writefile(path, sprintf('%J\n', cache)) != null) rename(path, CACHE_FILE);
	unlink(path);
}

function decryptTxt(record) {
	if (type(record) !== 'string' || length(record) > 16384) return null;
	const input = mkstemp();
	if (!input) return null;
	input.write(record);
	input.seek(0);
	const out = executeCommand(`${CRYPT_BIN} open-txt </dev/fd/${input.fileno()}`);
	input.close();
	return out.exitcode === 0 ? out.stdout : null;
}

function retryable(res) {
	return !!res.error || !res.status || (res.status >= 300 && res.status < 400) || res.status === 408 || res.status >= 500;
}

function apiHttp(method, url, headers, body, timeout, proxy) {
	return httpRequest(method, url, headers, body, timeout, proxy, false);
}

/* Inject I/O for deterministic tests of discovery, caching and request replay. */
export function createEndpointResolver(io) {
	io = io || {};
	const now = io.now || time, http = io.http || apiHttp;
	const decrypt = io.decrypt || decryptTxt, save = io.save || saveCache;
	const raw = (io.load || readCache)();
	const cache = {
		version: 1,
		fallbacks: raw?.version === 1 ? origins(raw.fallbacks) : [],
		active: DEFAULT_API_BASE, updated_at: 0, failed_at: 0, healthy_at: 0
	};
	if (raw?.version === 1) {
		for (let key in [ 'updated_at', 'failed_at', 'healthy_at' ])
			if (type(raw[key]) === 'int' && raw[key] > 0 && raw[key] <= now()) cache[key] = raw[key];
		if (raw.active in [ DEFAULT_API_BASE, STATIC_API_BASE, ...cache.fallbacks ]) cache.active = raw.active;
	}
	function candidates() {
		return origins([ cache.active, DEFAULT_API_BASE, STATIC_API_BASE, ...cache.fallbacks ]);
	}
	function healthy(base) {
		cache.active = base;
		cache.healthy_at = now();
		save(cache);
	}
	function failed(base) {
		if (cache.active === base) {
			cache.healthy_at = 0;
			cache.active = filter(candidates(), (v) => v !== base)[0] || DEFAULT_API_BASE;
			save(cache);
		}
	}
	function refresh(deadline, force, proxy) {
		if (cache.failed_at && now() - cache.failed_at < TXT_RETRY) return;
		if (!force && cache.updated_at && now() - cache.updated_at < TXT_TTL) return;
		/* Bound discovery to six seconds, reserving time for the actual API call. */
		const until = min(deadline - 1, now() + 6);
		for (let server in DNS_SERVERS) {
			if (now() >= until) break;
			const res = http('GET', server + '?name=' + TXT_HOST + '&type=TXT',
				{ Accept: 'application/dns-json, application/json' }, null, min(2, until - now()), proxy);
			if (res.error || res.status !== 200 || length(res.body || '') > 65536) continue;
			let data;
			try { data = json(res.body); } catch (e) { continue; }
			if (data?.Status !== 0 || type(data.Answer) !== 'array') continue;
			let found = [];
			for (let answer in slice(data.Answer, 0, 32)) {
				if (answer?.type !== 16 || type(answer.data) !== 'string') continue;
				const record = trim(replace(replace(answer.data, /^"|"$/g, ''), /"[[:space:]]+"/g, ''));
				found = origins([ ...found, ...parseTxtOrigins(decrypt(record)) ]);
			}
			if (!length(found)) continue;
			cache.fallbacks = found;
			cache.updated_at = now();
			cache.failed_at = 0;
			if (!(cache.active in [ DEFAULT_API_BASE, STATIC_API_BASE, ...found ])) {
				cache.active = DEFAULT_API_BASE;
				cache.healthy_at = 0;
			}
			save(cache);
			return true;
		}
		cache.failed_at = now();
		save(cache);
		return true;
	}
	function request(method, path, headers, body, timeout, proxy, replayable) {
		const deadline = now() + max(1, min(int(timeout) || 25, 60));
		const refreshed = refresh(deadline, false, proxy);
		const tried = [];
		for (let round = 0; round < 2; round++) {
			for (let base in candidates()) {
				if (base in tried || now() >= deadline) continue;
				push(tried, base);
				/* Never replay a login/code/legacy purchase after a possibly accepted write.
				 * Select a live origin with a public GET first; only POST once. */
				if (!replayable && !(base === cache.active && cache.healthy_at && now() - cache.healthy_at < HEALTH_TTL)) {
					const probe = http('GET', base + '/v1/common/site/config', { Accept: 'application/json' },
						null, min(5, deadline - now()), proxy);
					let data;
					try { data = json(probe.body || 'null'); } catch (e) { data = null; }
					if (probe.error || probe.status !== 200 || type(data) !== 'object'
						|| ('code' in data && !(data.code in [ 0, 200 ]))) { failed(base); continue; }
					healthy(base);
				}
				if (now() >= deadline) break;
				const res = http(method, base + path, headers, body, min(replayable ? 8 : 15, deadline - now()), proxy);
				if (!retryable(res)) { healthy(base); return res; }
				failed(base);
				if (!replayable) return { ...res, error: 'api_unavailable' };
			}
			if (round === 0 && !refreshed && now() < deadline) refresh(deadline, true, proxy);
		}
		return { status: 0, body: '', error: 'api_unavailable' };
	}
	return { current: () => cache.active, request };
};

export function currentApiBase() { return createEndpointResolver().current(); };
export function requestApi(method, path, headers, body, timeout, proxy, replayable) {
	return createEndpointResolver().request(method, path, headers, body, timeout, proxy, replayable);
};
