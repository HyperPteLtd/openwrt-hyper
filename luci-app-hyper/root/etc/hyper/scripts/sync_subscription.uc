#!/usr/bin/ucode
/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Sync nodes and routing rules from the logged-in Hyper account. Same flow as
 * the Hyper app (hyper-tunnel packages/app-client/src/subscribe/client.ts):
 *
 *   1. GET /v1/public/user/subscribe         -> subscription token (device session only)
 *   2. GET <api>/api/subscribe/location?token=        -> countries with nodes
 *   3. per country: hyper-crypt keygen, then
 *      GET <api>/api/subscribe?token=&country=&client_public_key=  -> { spk, salt, nonce, data }
 *      hyper-crypt open ... info=subscribe-v1         -> "REMARKS=/STATUS=/<share links>" lines
 *      (supported share-link formats are listed in snapshot.uc)
 *   4. GET <api>/api/subscribe/rules (JWT)   -> rule catalog -> /etc/hyper/rules/*.json
 *   5. write UCI `node` sections, restart if anything changed
 *
 * A failed fetch keeps the previous nodes; only a successful sync replaces them.
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

'use strict';

import { md5 } from 'md5';	/* plain-ucode MD5: 23.05 has no ucode-mod-digest */
import { mkstemp, open, readfile, writefile } from 'fs';
import { cursor } from 'uci';

import {
	executeCommand, getTime, isEmpty, shellQuote, httpRequest,
	readSession, CRYPT_BIN, HP_DIR, RUN_DIR, SINGBOX_BIN, SYNC_STATE_FILE
} from 'hyper';
import { request, refreshSubscriptionToken, subscribeEndpoint } from 'account_api';
import { parseFeatures, parseLocations, parseStatus, parseSubscription } from 'snapshot';
import { normalizeCatalog, parseRuleList, toRuleSetSources } from 'rules';

const uciconfig = 'hyper';
const ucimain = 'config', ucinode = 'node', ucisubscription = 'subscription';
const GROUP = 'ppanel';
const RULES_DIR = `${HP_DIR}/rules`;
const RULE_STORAGE = `${HP_DIR}/scripts/rules-storage.sh`;
const SUBSCRIBE_INFO = 'subscribe-v1';
const MAX_COUNTRIES = 64;

system(`mkdir -p ${RUN_DIR}`);
function log(...args) {
	const logfile = open(`${RUN_DIR}/hyper.log`, 'a');
	logfile.write(`${getTime()} [SYNC] ${join(' ', args)}\n`);
	logfile.close();
}

function save_state(state) {
	/* Background responses must not update state after logout or account changes. */
	if (!same_session()) return;
	writefile(SYNC_STATE_FILE, sprintf('%J\n', { time: time(), ...state }));
}

function crypt_keygen() {
	const out = executeCommand(CRYPT_BIN, 'keygen');
	if (out.exitcode !== 0)
		return null;
	try {
		return json(out.stdout);
	} catch (e) {
		return null;
	}
}

function crypt_open(private_key, env) {
	/*
	 * The ciphertext can exceed the 128 KiB single-argument limit, so it goes
	 * through a temp file on stdin; the (ephemeral) key goes through the environment.
	 */
	const enc = `${RUN_DIR}/subscribe.enc`;
	writefile(enc, env.data);

	const cmd = sprintf('HYPER_CRYPT_PRIVATE_KEY=%s %s open %s %s %s %s < %s',
		shellQuote(private_key), CRYPT_BIN,
		shellQuote(env.spk), shellQuote(env.salt), shellQuote(env.nonce), shellQuote(SUBSCRIBE_INFO),
		shellQuote(enc));
	const out = executeCommand(cmd);
	system(`rm -f ${shellQuote(enc)}`);

	return (out.exitcode === 0) ? out.stdout : null;
}

/* Plain body, or an encrypted { spk, salt, nonce, data } envelope */
function decrypt_body(body, keypair) {
	let env;
	try {
		env = json(body);
	} catch (e) {
		return body;
	}
	if (type(env) === 'string')
		return env;
	if (type(env) !== 'object' || isEmpty(env.spk) || isEmpty(env.salt) || isEmpty(env.nonce) || isEmpty(env.data))
		return null;
	return crypt_open(keypair.private_key, env);
}

function proxy_url(uci) {
	/* Background reads choose their own usable connection; no separate UI flag. */
	if (system('/etc/init.d/hyper running >/dev/null 2>&1') !== 0)
		return null;
	return 'socks5h://127.0.0.1:' + int(uci.get(uciconfig, 'infra', 'mixed_port') || '5330');
}

let available_proxy = null, selected_proxy = null, sync_session = null;
function same_session() {
	return !!sync_session && readSession()?.jwt === sync_session;
}
function read_auto(fn) {
	if (!same_session())
		return { ok: false, status: 0, error: 'session_changed' };
	let res = fn(selected_proxy);
	if (!same_session())
		return { ok: false, status: 409, error: 'session_changed' };
	/* Only retry connection/server failures, never authentication or invalid data. */
	if (available_proxy && (!res.status || res.status === 408 || res.status >= 500)
		&& !res.ok && same_session()) {
		const alternate = selected_proxy ? null : available_proxy;
		res = fn(alternate);
		if (!same_session())
			return { ok: false, status: 409, error: 'session_changed' };
		if (res.ok || (res.status >= 200 && res.status < 300))
			selected_proxy = alternate;
	}
	return res;
}
function sync_request(method, path, opts) {
	return read_auto((proxy) => request(method, path, { ...opts, proxy }));
}

/* Build tags of the installed sing-box (with_quic, with_wireguard, ...), null if unknown */
function singbox_features() {
	const out = executeCommand(SINGBOX_BIN, 'version');
	return (out.exitcode === 0) ? parseFeatures(out.stdout) : null;
}

function fetch_country(ep, token, country, opts, proxy) {
	const keypair = crypt_keygen();
	if (!keypair?.public_key)
		return { error: 'crypt_unavailable' };

	const res = sync_request('GET', ep.path, {
		auth: false, raw: true, timeout: 30, proxy,
		query: { token, country, client_public_key: keypair.public_key }
	});
	if (!res.ok)
		return { error: `HTTP ${res.status || 0}` };

	const body = decrypt_body(res.body, keypair);
	if (body == null)
		return { error: 'decrypt' };

	return parseSubscription(body, { ...opts, country, group: GROUP });
}

/* Ownership and mode are set on the temporary file before atomic publication. */
function publish_rule(name, content) {
	const input = mkstemp();
	if (!input)
		return false;
	if (input.write(content) !== length(content)) {
		input.close();
		return false;
	}
	input.seek(0);
	const result = system(`sh ${shellQuote(RULE_STORAGE)} publish ${shellQuote(name)} < /dev/fd/${input.fileno()}`);
	input.close();
	return result === 0;
}

/* Rule catalog -> rule-set source files + index.json read by generate_client.uc */
function sync_rules(ep, proxy) {
	const res = sync_request('GET', ep.path + '/rules', { timeout: 20 });
	if (!res.ok) {
		log(sprintf('Rules: fetch failed (HTTP %d), keeping the previous rules.', res.status || 0));
		return { ok: false };
	}

	const list = (type(res.data) === 'array') ? res.data : (res.data?.list || []);
	const cat = normalizeCatalog(list);
	for (let e in cat.errors)
		log('Rules:', e);

	const index = [];
	let previous;
	try {
		previous = json(readfile(`${RULES_DIR}/index.json`) || '[]');
	} catch (e) {
		previous = [];
	}
	if (system(`sh ${shellQuote(RULE_STORAGE)} prepare`) !== 0) {
		log('Rules: cannot prepare the rule directory, keeping the previous index.');
		return { ok: false };
	}
	for (let e in cat.entries) {
		let text = e.inline;
		if (e.url) {
			const r = read_auto((proxy) => httpRequest('GET', e.url, { 'User-Agent': 'luci-app-hyper' }, null, 30, proxy));
			if (r.status !== 200) {
				/* keep the previous copy of this list if there is one */
				const prev = filter(previous, (p) => p.key === e.key)[0];
				log(sprintf('Rules: %s download failed (HTTP %d)%s.', e.key, r.status, prev ? ', keeping the previous copy' : ''));
				if (prev)
					push(index, { ...prev, policy: e.policy, enabled: e.enabled, sort: e.sort, stale: true });
				continue;
			}
			text = r.body;
		}

		if (!same_session()) return { ok: false };
		const parsed = parseRuleList(text);
		const sources = toRuleSetSources(parsed);
		for (let kind in [ 'domain', 'ip' ])
			if (sources[kind] && !publish_rule(`${e.key}.${kind}.json`, sprintf('%J\n', sources[kind]))) {
				log('Rules: cannot publish a rule file, keeping the previous index.');
				return { ok: false };
			}
		push(index, {
			key: e.key, policy: e.policy, enabled: e.enabled, sort: e.sort,
			has_domain: !!sources.domain, has_ip: !!sources.ip,
			geoip: parsed.geoip, count: parsed.count, skipped: parsed.skipped,
			source: e.url ? 'url' : 'inline'
		});
	}

	const before = readfile(`${RULES_DIR}/index.json`) || '';
	const after = sprintf('%J\n', index);
	if (!same_session() || !publish_rule('index.json', after)) {
		log('Rules: cannot publish the rule index, keeping the previous index.');
		return { ok: false };
	}

	/* Only remove old files after the new index is safely published. */
	const keep = {};
	for (let e in index) {
		keep[`${e.key}.domain.json`] = true;
		keep[`${e.key}.ip.json`] = true;
	}
	for (let f in split(trim(executeCommand('ls', RULES_DIR).stdout || ''), '\n'))
		if (f !== '' && f !== 'index.json' && !keep[f] && match(f, /^[A-Za-z0-9_.-]+$/))
			system(`rm -f ${shellQuote(RULES_DIR + '/' + f)}`);

	log(sprintf('Rules: %d lists.', length(index)));
	return { ok: true, count: length(index), changed: before !== after };
}

function main() {
	const session = readSession();
	sync_session = session?.jwt;

	if (!session?.jwt) {
		log('Not signed in, skipping.');
		save_state({ ok: false, error: 'not_signed_in' });
		return 1;
	}

	const uci = cursor();
	uci.load(uciconfig);
	available_proxy = proxy_url(uci);
	const proxy = null;
	const parse_opts = {
		/* AnyTLS links without an insecure flag skip certificate checks, like the app */
		insecure_default: (uci.get(uciconfig, ucisubscription, 'allow_insecure') ?? '1') === '1',
		features: singbox_features()
	};

	const tok = read_auto((proxy) => refreshSubscriptionToken(proxy));
	if (!tok.ok) {
		log('Failed to get the subscription token:', tok.msg || tok.error);
		save_state({ ok: false, error: tok.error || 'subscription_token', msg: tok.msg });
		return 1;
	}
	if (!same_session()) return 1;
	const token = readSession()?.sub_token;
	const ep = subscribeEndpoint();

	/* Countries */
	const loc = sync_request('GET', ep.path + '/location', { auth: false, timeout: 20, query: { token } });
	const countries = loc.ok ? parseLocations(loc.data) : [];
	if (!loc.ok || !length(countries)) {
		log(sprintf('Failed to list countries (HTTP %d).', loc.status || 0));
		save_state({ ok: false, error: loc.ok ? 'no_countries' : 'locations', status: loc.status });
		return 1;
	}

	/* Nodes, country by country */
	const fresh = {};
	let invalid = 0, failed = [], status = null, remarks = null, unsupported = {};
	for (let c in slice(countries, 0, MAX_COUNTRIES)) {
		const r = fetch_country(ep, token, c.code, parse_opts, proxy);
		if (r.error) {
			if (r.error === 'crypt_unavailable') {
				log(`hyper-crypt is missing (${CRYPT_BIN}), reinstall luci-app-hyper.`);
				save_state({ ok: false, error: 'crypt_unavailable' });
				return 1;
			}
			push(failed, c.code);
			log(`Country ${c.code}: ${r.error}.`);
			continue;
		}
		invalid += r.invalid;
		for (let u in r.unsupported)
			unsupported[u] = (unsupported[u] || 0) + 1;
		status = status || r.status;
		remarks = remarks || r.remarks;
		for (let node in r.nodes) {
			node.country_sort = `${c.sort}`;
			fresh['pp_' + substr(md5(`${c.code}|${node.type}|${node.address}|${node.port}|${node.label}`), 0, 16)] = node;
		}
	}

	/* Every country failed: keep what we have */
	if (length(failed) === length(slice(countries, 0, MAX_COUNTRIES))) {
		log('Every country failed, keeping the previous nodes.');
		save_state({ ok: false, error: 'nodes', failed });
		return 1;
	}

	/* Discard a stale account response and reload the latest user selections. */
	if (!same_session()) return 1;
	uci.unload(uciconfig);
	uci.load(uciconfig);
	/* Compare with what UCI has now */
	const before = {};
	uci.foreach(uciconfig, ucinode, (s) => {
		if (s.grouphash === GROUP)
			before[s['.name']] = s;
	});

	let added = 0, removed = 0, changed = false;
	for (let name in before)
		/* nodes of a country that failed this time stay until it answers again */
		if (!fresh[name] && !(before[name].country in failed)) {
			uci.delete(uciconfig, name);
			removed++;
			changed = true;
		}

	for (let name in fresh) {
		const node = fresh[name];
		const old = before[name];
		if (!old) {
			uci.set(uciconfig, name, ucinode);
			added++;
			changed = true;
		} else
			/* options the link no longer carries */
			for (let k in old)
				if (substr(k, 0, 1) !== '.' && k !== 'country_sort' && !(k in node)) {
					uci.delete(uciconfig, name, k);
					changed = true;
				}
		for (let k in node) {
			const v = node[k];
			if (v == null) {
				if (old?.[k] != null) {
					uci.delete(uciconfig, name, k);
					changed = true;
				}
			} else if (sprintf('%J', old?.[k]) !== sprintf('%J', v)) {
				uci.set(uciconfig, name, k, v);
				changed = true;
			}
		}
	}

	/* Keep main_node pointing at a node that still exists (first node of the top country otherwise) */
	const main_node = uci.get(uciconfig, ucimain, 'main_node') || 'nil';
	if (main_node !== 'urltest' && uci.get(uciconfig, main_node) !== ucinode) {
		let first = null;
		for (let name in fresh)
			if (!first || int(fresh[name].country_sort) > int(fresh[first].country_sort))
				first = name;
		uci.set(uciconfig, ucimain, 'main_node', first || 'nil');
		changed = true;
		log(first ? `Main node is gone, switching to ${fresh[first].label}.` : 'No node available.');
	}

	if (!same_session()) return 1;
	uci.commit(uciconfig);

	if (!same_session()) return 1;
	const rules = sync_rules(ep, proxy);
	if (!same_session()) return 1;

	const count = length(keys(fresh));
	log(sprintf('%d nodes from %d countries (%d added, %d removed, %d invalid links).',
		count, length(countries) - length(failed), added, removed, invalid));
	for (let u in unsupported)
		log(sprintf('Skipped %d link(s): %s.', unsupported[u], u));
	save_state({
		ok: true, count, added, removed, invalid,
		unsupported: length(keys(unsupported)) ? unsupported : null,
		countries: map(countries, (c) => c.code), failed,
		remarks, status, ...parseStatus(status),
		rules: rules.ok ? rules.count : null
	});

	const latest = cursor();
	latest.load(uciconfig);
	if (same_session() && (changed || rules.changed) && latest.get(uciconfig, ucimain, 'enabled') === '1') {
		log('Restarting service...');
		system("ubus call luci.hyper restart '{}' >/dev/null 2>&1");
	}

	return 0;
}

try {
	exit(main());
} catch (e) {
	log('[FATAL ERROR]', sprintf('%s: %s', e.type, e.message));
	save_state({ ok: false, error: 'fatal', msg: e.message });
	exit(1);
}
