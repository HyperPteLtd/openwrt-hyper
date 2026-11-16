/** Execute the production ucode functions with deterministic HTTP/session/UCI seams. */
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('../luci-app-hyper/root/etc/hyper/scripts/sync_subscription.uc', import.meta.url), 'utf8');
const accountSource = readFileSync(new URL('../luci-app-hyper/root/etc/hyper/scripts/account_api.uc', import.meta.url), 'utf8');
function section(text, start, end) {
  const first = text.indexOf(start), last = text.indexOf(end, first + start.length);
  assert.ok(first >= 0 && last > first, `missing production section: ${start}`);
  return text.slice(first, last);
}
const adaptive = section(source, 'let available_proxy', '/* Build tags');
const token = section(accountSource, 'export function refreshSubscriptionToken(', '/* Email verification').replace('export function', 'function');
const country = section(source, 'function fetch_country(', '/* Ownership');
const main = section(source, 'function main()', '\ntry {');
const saveState = section(source, 'function save_state(', '\nfunction crypt_keygen(');
const ucode = process.env.UCODE || 'ucode';
function run(code) {
  const result = spawnSync(ucode, ['-S', '-e', code], { encoding: 'utf8', timeout: 10000 });
  if (result.error) throw result.error;
  assert.equal(result.status, 0, result.stderr);
  return JSON.parse(result.stdout);
}
const sessionPrelude = `
let session = { jwt: 'account-a', sub_token: 'old-token' }, writes = [];
function readSession() { return session; }
function writeSession(value) { session = value; push(writes, value); }
function isEmpty(value) { return value == null || value === ''; }
`;
function connectionCase(body) {
  return run(`${sessionPrelude}
function request() { die('unexpected API request'); }
${adaptive}
sync_session = 'account-a'; available_proxy = 'socks5h://fixture:5330';
const calls = [];
${body}
`);
}

test('direct success never retries via the running proxy', () => {
  const result = connectionCase(`const result = read_auto((proxy) => { push(calls, proxy); return { ok: true, status: 200 }; }); print(sprintf('%J', { result, calls }));`);
  assert.equal(result.result.ok, true);
  assert.deepEqual(result.calls, [null]);
});

test('transport failure falls back to the running proxy and reuses it for later reads', () => {
  const result = connectionCase(`
function read(proxy) { push(calls, proxy); return proxy ? { ok: true, status: 200 } : { ok: false, status: 0, error: 'connect' }; }
read_auto(read); read_auto(read); print(sprintf('%J', calls));`);
  assert.deepEqual(result, [null, 'socks5h://fixture:5330', 'socks5h://fixture:5330']);
});

test('without a running proxy a transport failure is returned once', () => {
  const result = connectionCase(`available_proxy = null; const result = read_auto((proxy) => { push(calls, proxy); return { ok: false, status: 0 }; }); print(sprintf('%J', { result, calls }));`);
  assert.equal(result.result.ok, false);
  assert.deepEqual(result.calls, [null]);
});

test('authentication, subscription absence and malformed successful responses are not retried', () => {
  for (const response of [{ ok: false, status: 401 }, { ok: false, status: 200, error: 'no_subscription' }, { ok: false, status: 200, error: 'invalid_json' }]) {
    const result = connectionCase(`const result = read_auto((proxy) => { push(calls, proxy); return ${JSON.stringify(response)}; }); print(sprintf('%J', { result, calls }));`);
    assert.deepEqual(result.calls, [null]);
    assert.deepEqual(result.result, response);
  }
});

test('logout or account replacement during either connection discards its response', () => {
  for (const identity of [null, { jwt: 'account-b' }]) for (const onFallback of [false, true]) {
    const result = connectionCase(`
const result = read_auto((proxy) => {
  push(calls, proxy);
  if (${onFallback} && !proxy) return { ok: false, status: 0 };
  session = ${JSON.stringify(identity)}; return { ok: true, status: 200, data: 'old-account-data' };
}); print(sprintf('%J', { result, calls }));`);
    assert.equal(result.result.error, 'session_changed');
    assert.equal(result.result.status, 409);
    assert.equal(result.result.data, undefined);
    assert.equal(result.calls.length, onFallback ? 2 : 1);
  }
});

test('token refresh propagates HTTP failures and never overwrites a replaced session', () => {
  for (const status of [0, 401, 503]) {
    const result = run(`${sessionPrelude} function request() { return { ok: false, status: ${status}, error: 'failure' }; } ${token} print(sprintf('%J', { result: refreshSubscriptionToken(null), writes }));`);
    assert.equal(result.result.status, status);
    assert.equal(result.writes.length, 0);
  }
  for (const identity of [null, { jwt: 'account-b', sub_token: 'new-account-token' }]) {
    const result = run(`${sessionPrelude} function request() { session = ${JSON.stringify(identity)}; return { ok: true, data: { list: [{ id: 1, status: 1, token: 'stale-token' }] } }; } ${token} print(sprintf('%J', { result: refreshSubscriptionToken(null), session, writes }));`);
    assert.equal(result.result.error, 'session_changed');
    assert.deepEqual(result.session, identity);
    assert.equal(result.writes.length, 0);
  }
  const noSubscription = run(`${sessionPrelude} function request() { return { ok: true, data: { list: [] } }; } ${token} print(sprintf('%J', refreshSubscriptionToken(null)));`);
  assert.deepEqual(noSubscription, { ok: false, status: 200, error: 'no_subscription' });
  const replacedBeforeWrite = run(`
let session = { jwt: 'account-a' }, reads = 0, writes = 0;
function readSession() { if (++reads === 3) session = { jwt: 'account-b' }; return session; }
function writeSession() { writes++; }
function isEmpty(value) { return value == null || value === ''; }
function request() { return { ok: true, data: { list: [{ status: 1, token: 'stale-token' }] } }; }
${token}
print(sprintf('%J', { result: refreshSubscriptionToken(null), session, writes }));`);
  assert.equal(replacedBeforeWrite.result.error, 'session_changed');
  assert.equal(replacedBeforeWrite.session.jwt, 'account-b');
  assert.equal(replacedBeforeWrite.writes, 0);
});

function synchronize(scenario = {}) {
  return run(`
${sessionPrelude}
const scenario = ${JSON.stringify(scenario)};
const uciconfig = 'hyper', ucimain = 'config', ucinode = 'node', ucisubscription = 'subscription';
const GROUP = 'ppanel', MAX_COUNTRIES = 64, CRYPT_BIN = '/fixture/crypt', SYNC_STATE_FILE = '/fixture/state';
let db = {
  config: { '.type': 'hyper', enabled: '1', main_node: 'old' },
  old: { '.type': 'node', grouphash: GROUP, country: 'USA', label: 'old node' },
  manual: { '.type': 'node', label: 'manual node', address: 'manual.example' }
};
const saved = [], calls = [], restarts = [];
let commits = 0, ruleReads = 0;
function clone(value) { return json(sprintf('%J', value)); }
function cursor() {
  let local = null, changes = [];
  return {
    load: () => { local = clone(db); return true; }, unload: () => { local = null; changes = []; },
    get: (conf, name, option) => option == null ? local[name]?.['.type'] : local[name]?.[option],
    foreach: (conf, kind, fn) => { for (let name in local) if (local[name]['.type'] === kind) fn({ ...local[name], '.name': name }); },
    set: (conf, name, option, value) => { if (value == null) local[name] = { '.type': option }; else local[name][option] = value; push(changes, [name, option, value]); },
    delete: (conf, name, option) => { if (option == null) delete local[name]; else delete local[name][option]; push(changes, [name, option, null, true]); },
    commit: () => { for (let c in changes) { if (c[3]) { if (c[1] == null) delete db[c[0]]; else delete db[c[0]][c[1]]; } else if (c[2] == null) db[c[0]] = { '.type': c[1] }; else db[c[0]][c[1]] = c[2]; } commits++; changes = []; }
  };
}
function log(...args) {}
function writefile(path, value) { push(saved, json(value)); return length(value); }
function system(command) { push(restarts, command); return 0; }
function proxy_url(uci) { return scenario.no_proxy ? null : 'socks5h://fixture:5330'; }
function singbox_features() { return null; }
function refreshSubscriptionToken() { return { ok: true }; }
function subscribeEndpoint() { return { path: '/api/subscribe' }; }
function parseLocations(value) { return value; }
function crypt_keygen() { return { public_key: 'test-key' }; }
function decrypt_body(value) { return value; }
function parseSubscription() { return { nodes: [{ type: 'anytls', address: 'new.example', port: '443', label: 'new node', country: 'USA', grouphash: GROUP }], invalid: 0, unsupported: {} }; }
function parseStatus() { return {}; }
function md5() { return 'fresh'; }
function request(method, path, options) {
  push(calls, { path, proxy: options.proxy });
  if (path === '/api/subscribe/location') return { ok: true, status: 200, data: [{ code: 'USA', sort: 1 }] };
  if (scenario.stop) db.config.enabled = '0';
  if (scenario.select) db.config.main_node = scenario.select;
  if (scenario.logout) session = null;
  if (scenario.account_change) session = { jwt: 'account-b', sub_token: 'new-token' };
  return scenario.fail_nodes ? { ok: false, status: 0 } : { ok: true, status: 200, body: 'fixture nodes' };
}
function sync_rules() { ruleReads++; if (scenario.logout_during_rules) session = null; if (scenario.stop_during_rules) db.config.enabled = '0'; return { ok: true, count: 1, changed: true }; }
${adaptive}
${saveState}
${country}
${main}
const code = main(); print(sprintf('%J', { code, db, saved, calls, restarts, commits, ruleReads }));
`);
}

test('both node connections failing retains old nodes, selection and rules', () => {
  const result = synchronize({ fail_nodes: true });
  assert.equal(result.code, 1);
  assert.equal(result.commits, 0);
  assert.equal(result.ruleReads, 0);
  assert.equal(result.db.old.label, 'old node');
  assert.equal(result.db.config.main_node, 'old');
  assert.equal(result.saved[0].error, 'nodes');
  assert.deepEqual(result.calls.filter(c => c.path === '/api/subscribe').map(c => c.proxy), [null, 'socks5h://fixture:5330']);
  assert.deepEqual(result.restarts, []);
});

test('completed background sync preserves the latest Stop and a valid manual selection', () => {
  const result = synchronize({ stop: true, select: 'manual' });
  assert.equal(result.code, 0);
  assert.equal(result.db.config.enabled, '0');
  assert.equal(result.db.config.main_node, 'manual');
  assert.ok(result.db.pp_fresh);
  assert.equal(result.db.old, undefined);
  assert.deepEqual(result.restarts, []);
  const stoppedDuringRules = synchronize({ stop_during_rules: true });
  assert.equal(stoppedDuringRules.db.config.enabled, '0');
  assert.deepEqual(stoppedDuringRules.restarts, []);
});

test('a removed selected node is replaced, while the automatic selector stays selected', () => {
  const replaced = synchronize();
  assert.equal(replaced.code, 0);
  assert.equal(replaced.db.config.main_node, 'pp_fresh');
  assert.equal(replaced.restarts.length, 1);
  const automatic = synchronize({ select: 'urltest' });
  assert.equal(automatic.db.config.main_node, 'urltest');
});

test('old-account sync never commits or publishes state after logout or replacement', () => {
  for (const scenario of [{ logout: true }, { account_change: true }]) {
    const result = synchronize(scenario);
    assert.equal(result.code, 1);
    assert.equal(result.commits, 0);
    assert.equal(result.db.old.label, 'old node');
    assert.deepEqual(result.saved, []);
    assert.deepEqual(result.restarts, []);
  }
  const result = synchronize({ logout_during_rules: true });
  assert.equal(result.code, 1);
  assert.deepEqual(result.saved, []);
  assert.deepEqual(result.restarts, []);
});
