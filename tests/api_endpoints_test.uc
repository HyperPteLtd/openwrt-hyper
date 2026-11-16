import { eq, ok, done } from 'lib';
import { lstat, readfile, unlink } from 'fs';
import { CRYPT_BIN, RUN_DIR } from 'hyper';
import { createEndpointResolver, normalizeApiOrigin, parseTxtOrigins, DEFAULT_API_BASE, STATIC_API_BASE, TXT_HOST } from 'api_endpoints';

const DYNAMIC = 'https://api-new.example';
const success = { status: 200, body: '{"code":200,"msg":"ok","data":{}}' };
const offline = { status: 0, body: '', error: 'timeout' };
function dns(record) { return { status: 200, body: sprintf('%J', { Status: 0, Answer: [ { type: 16, data: record } ] }) }; }
function fresh(extra) { return { version: 1, updated_at: 10000, ...extra }; }
function fixture(cache, handle) {
	const state = { now: 10000, disk: cache, calls: [], records: [] };
	const io = {
		now: () => state.now,
		load: () => state.disk,
		save: (v) => { state.disk = json(sprintf('%J', v)); },
		decrypt: (v) => { push(state.records, v); return v === 'authenticated' ? 'v=1|api-new.example' : null; },
		http: (method, url, headers, body, timeout, proxy) => {
			const call = { method, url, headers, body, timeout, proxy };
			push(state.calls, call);
			return handle(call, state);
		}
	};
	return { state, resolver: createEndpointResolver(io), reopen: () => createEndpointResolver(io) };
}
function isDNS(c) { return index(c.url, '?name=' + TXT_HOST + '&type=TXT') >= 0; }
function get(f) { return f.resolver.request('GET', '/v1/public/user/info', { Authorization: 'Bearer test' }, null, 25, null, true); }

eq('bare domain', normalizeApiOrigin('api.example.com'), 'https://api.example.com');
eq('HTTPS origin normalization', normalizeApiOrigin(' HTTPS://Api.Example.com/ '), 'https://api.example.com');
for (let value in [ null, 42, '', 'https://', 'http://api.example.com', 'https://a.example:8443',
	'https://a.example/path', 'https://a.example?token=x', 'https://a.example/#x', 'https://user@a.example',
	'https://a.example\nattack', 'https://a.example\u0000', 'https://-a.example', 'https://a..example', 'https://[::1]' ])
	eq(`reject origin ${value}`, normalizeApiOrigin(value), null);
eq('versioned TXT list, dedup and reject unsafe URLs', parseTxtOrigins('v=1|api.example.com|https://api.example.com/;http://bad.example,api-new.example'),
	[ 'https://api.example.com', DYNAMIC ]);
eq('unversioned TXT rejected', parseTxtOrigins('api.example.com'), []);
eq('default present without any configuration', createEndpointResolver({ load: () => null }).current(), DEFAULT_API_BASE);
eq('untrusted active address discarded', createEndpointResolver({ load: () => fresh({ active: 'https://other.example' }) }).current(), DEFAULT_API_BASE);

let f = fixture(null, (c) => isDNS(c) ? dns('"authen" "ticated"') : index(c.url, DYNAMIC) === 0 ? success : offline);
eq('TXT origin recovers failed built-in addresses', get(f).status, 200);
eq('DNS TXT chunks concatenated', f.state.records, [ 'authenticated' ]);
eq('successful origin remembered', f.reopen().current(), DYNAMIC);
eq('TXT queried once', length(filter(f.state.calls, isDNS)), 1);
ok('DNS never receives account credentials', !filter(f.state.calls, isDNS)[0].headers.Authorization);
eq('read attempts primary, static, dynamic', map(filter(f.state.calls, (c) => !isDNS(c)), (c) => c.url),
	map([ DEFAULT_API_BASE, STATIC_API_BASE, DYNAMIC ], (base) => base + '/v1/public/user/info'));

f = fixture(null, (c) => isDNS(c) ? (index(c.url, 'dns.alidns.com') >= 0 ? dns('plaintext') : dns('authenticated')) : success);
eq('bad TXT does not prevent next provider', get(f).status, 200);
eq('second DNS provider used for unauthenticated answer', length(filter(f.state.calls, isDNS)), 2);

f = fixture(fresh({ updated_at: 1, fallbacks: [DYNAMIC], active: DYNAMIC }), (c) => isDNS(c) ? offline : success);
eq('DNS failure retains cached origin', get(f).status, 200);
eq('stale TXT fallbacks retained', f.state.disk.fallbacks, [DYNAMIC]);
const attempts = length(f.state.calls);
f.resolver = f.reopen();
get(f);
eq('failed DNS backed off', length(f.state.calls), attempts + 1);
f.state.now += 301;
f.resolver = f.reopen();
get(f);
eq('DNS retried after five minutes', length(filter(f.state.calls, isDNS)), 6);

f = fixture(fresh({}), (c) => isDNS(c) ? dns('authenticated') : success);
get(f);
eq('fresh cache avoids DNS', length(filter(f.state.calls, isDNS)), 0);
f.state.now += 1801;
f.resolver = f.reopen();
get(f);
eq('cache refreshed after thirty minutes', length(filter(f.state.calls, isDNS)), 1);

f = fixture(fresh({}), (c) => isDNS(c) ? dns('authenticated') : index(c.url, DYNAMIC) === 0 ? success : offline);
eq('all cached origins failing forces TXT refresh', get(f).status, 200);
eq('refresh only retries newly discovered origins', length(filter(f.state.calls, (c) => !isDNS(c))), 3);

for (let status in [ 401, 403, 429 ]) {
	f = fixture(fresh({}), () => ({ status, body: '{}' }));
	eq(`HTTP ${status} preserved`, get(f).status, status);
	eq(`HTTP ${status} not replayed`, length(f.state.calls), 1);
}

f = fixture(fresh({}), (c) => index(c.url, DEFAULT_API_BASE) === 0 ? offline : c.method === 'GET' ? success : offline);
let res = f.resolver.request('POST', '/v1/common/send_code', {}, { email: 'a@example.com' }, 25, 'socks5h://127.0.0.1:5330', false);
eq('ambiguous code send returns connection error', res.error, 'api_unavailable');
eq('code sent exactly once after healthy fallback probe', length(filter(f.state.calls, (c) => c.method === 'POST')), 1);
eq('write uses reachable fallback', filter(f.state.calls, (c) => c.method === 'POST')[0].url, STATIC_API_BASE + '/v1/common/send_code');
ok('proxy applies to probes and write', !length(filter(f.state.calls, (c) => c.proxy !== 'socks5h://127.0.0.1:5330')));
ok('preflight does not carry credentials or body', !f.state.calls[0].headers.Authorization && f.state.calls[0].body == null);

f = fixture(fresh({}), (c) => index(c.url, DEFAULT_API_BASE) === 0 ? { status: 503, body: '{}' } : success);
res = f.resolver.request('POST', '/v2/orders/purchase', { 'Idempotency-Key': 'checkout:test-key' }, { id: 123 }, 25, null, true);
eq('idempotent write recovers', res.status, 200);
eq('idempotency key and payload unchanged', map(f.state.calls, (c) => [c.method, c.headers['Idempotency-Key'], c.body]),
	[ ['POST', 'checkout:test-key', { id: 123 }], ['POST', 'checkout:test-key', { id: 123 }] ]);

f = fixture(null, (c, state) => { state.now += c.timeout; return offline; });
eq('complete outage reported', get(f).error, 'api_unavailable');
ok('discovery and attempts share a bounded deadline', f.state.now <= 10025);
ok('all HTTP timeouts positive', !length(filter(f.state.calls, (c) => c.timeout <= 0)));

/* Installed OpenWrt test: real helper, inherited stdin fd, and atomic RAM cache. */
if (lstat(CRYPT_BIN)) {
	const vector = 'AAECAwQFBgcICQoLgBKV3OnfGGdC9EUAaTiJA78Ndy9yi/kkkDNX5WeBLhTrJfrvZDDA1W/JzEpU48YGnh6n';
	const resolver = createEndpointResolver({
		load: () => null,
		http: (method, url) => index(url, '?name=') >= 0 ? dns(vector) : success
	});
	eq('real TXT helper request', resolver.request('GET', '/v1/common/site/config', {}, null, 25, null, true).status, 200);
	const stored = json(readfile(RUN_DIR + '/api-endpoints.json') || 'null');
	eq('real helper decrypts TXT and persists origins', stored?.fallbacks, [ 'https://api-one.example', 'https://api-two.example' ]);
	eq('cache reload after process boundary', createEndpointResolver().current(), DEFAULT_API_BASE);
	unlink(RUN_DIR + '/api-endpoints.json');
} else {
	print('skip installed TXT helper integration (runs in router-test.sh)\n');
}

done('api_endpoints');
