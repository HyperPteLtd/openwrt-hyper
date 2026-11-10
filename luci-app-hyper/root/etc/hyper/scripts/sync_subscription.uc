#!/usr/bin/ucode
/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Sync nodes and routing rules from the logged-in PPanel account. Same flow as
 * the Hyper app (hyper-tunnel packages/app-client/src/subscribe/client.ts):
 *
 *   1. GET /v1/public/user/subscribe         -> subscription token (device session only)
 *   2. GET <api>/api/subscribe/location?token=        -> countries with nodes
 *   3. per country: hyper-crypt keygen, then
 *      GET <api>/api/subscribe?token=&country=&client_public_key=  -> { spk, salt, nonce, data }
 *      hyper-crypt open ... info=subscribe-v1         -> "REMARKS=/STATUS=/<share links>" lines
 *      (every protocol sing-box supports, see snapshot.uc)
 *   4. GET <api>/api/subscribe/rules (JWT)   -> rule catalog -> /etc/hyper/rules/*.json
 *   5. write UCI `node` sections, restart if anything changed
 *
 * A failed fetch keeps the previous nodes; only a successful sync replaces them.
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

'use strict';

import { md5 } from 'md5';	/* plain-ucode MD5: 23.05 has no ucode-mod-digest */
import { open, readfile, writefile } from 'fs';
import { cursor } from 'uci';

import {
	executeCommand, getTime, isEmpty, shellQuote, httpRequest,
	readSession, CRYPT_BIN, HP_DIR, RUN_DIR, SINGBOX_BIN, SYNC_STATE_FILE
} from 'hyper';
import { config as ppanelConfig, request, refreshSubscriptionToken, subscribeEndpoint } from 'ppanel';
import { parseFeatures, parseLocations, parseStatus, parseSubscription } from 'snapshot';
import { normalizeCatalog, parseRuleList, toRuleSetSources } from 'rules';

const uciconfig = 'hyper';
const ucimain = 'config', ucinode = 'node', ucisubscription = 'subscription';
const GROUP = 'ppanel';
const RULES_DIR = `${HP_DIR}/rules`;
const SUBSCRIBE_INFO = 'subscribe-v1';
const MAX_COUNTRIES = 64;

system(`mkdir -p ${RUN_DIR}`);
function log(...args) {
	const logfile = open(`${RUN_DIR}/hyper.log`, 'a');
	logfile.write(`${getTime()} [SYNC] ${join(' ', args)}\n`);
	logfile.close();
}

function save_state(state) {
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
	/* fetch through sing-box when asked to, and only while it runs */
	if (uci.get(uciconfig, ucisubscription, 'update_via_proxy') !== '1')
		return null;
	if (system('/etc/init.d/hyper running >/dev/null 2>&1') !== 0)
		return null;
	return 'socks5h://127.0.0.1:' + int(uci.get(uciconfig, 'infra', 'mixed_port') || '5330');
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

	const res = request('GET', ep.path, {
		base: ep.base, auth: false, raw: true, timeout: 30, proxy,
		query: { token, country, client_public_key: keypair.public_key }
	});
	if (!res.ok)
		return { error: `HTTP ${res.status || 0}` };

	const body = decrypt_body(res.body, keypair);
	if (body == null)
		return { error: 'decrypt' };

	return parseSubscription(body, { ...opts, country, group: GROUP });
}

/* Rule catalog -> rule-set source files + index.json read by generate_client.uc */
function sync_rules(ep, proxy) {
	const res = request('GET', ep.path + '/rules', { base: ep.base, timeout: 20, proxy });
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
	system(`mkdir -p ${RULES_DIR}`);
	for (let e in cat.entries) {
		let text = e.inline;
		if (e.url) {
			const r = httpRequest('GET', e.url, { 'User-Agent': 'luci-app-hyper' }, null, 30, proxy);
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

		const parsed = parseRuleList(text);
		const sources = toRuleSetSources(parsed);
		for (let kind in [ 'domain', 'ip' ])
			if (sources[kind])
				writefile(`${RULES_DIR}/${e.key}.${kind}.json`, sprintf('%J\n', sources[kind]));
		push(index, {
			key: e.key, policy: e.policy, enabled: e.enabled, sort: e.sort,
			has_domain: !!sources.domain, has_ip: !!sources.ip,
			geoip: parsed.geoip, count: parsed.count, skipped: parsed.skipped,
			source: e.url ? 'url' : 'inline'
		});
	}

	/* drop files of rules that are gone */
	const keep = {};
	for (let e in index) {
		keep[`${e.key}.domain.json`] = true;
		keep[`${e.key}.ip.json`] = true;
	}
	for (let f in split(trim(executeCommand('ls', RULES_DIR).stdout || ''), '\n'))
		if (f !== '' && f !== 'index.json' && !keep[f] && match(f, /^[A-Za-z0-9_.-]+$/))
			system(`rm -f ${shellQuote(RULES_DIR + '/' + f)}`);

	const before = readfile(`${RULES_DIR}/index.json`) || '';
	const after = sprintf('%J\n', index);
	writefile(`${RULES_DIR}/index.json`, after);
	log(sprintf('Rules: %d lists.', length(index)));
	return { ok: true, count: length(index), changed: before !== after };
}

function main() {
	const cfg = ppanelConfig();
	const session = readSession();

	if (isEmpty(cfg.api_base) || !session?.jwt) {
		log('Not signed in, skipping.');
		save_state({ ok: false, error: 'not_signed_in' });
		return 1;
	}

	const uci = cursor();
	uci.load(uciconfig);
	const proxy = proxy_url(uci);
	const parse_opts = {
		/* AnyTLS links without an insecure flag skip certificate checks, like the app */
		insecure_default: (uci.get(uciconfig, ucisubscription, 'allow_insecure') ?? '1') === '1',
		features: singbox_features()
	};

	const tok = refreshSubscriptionToken();
	if (!tok.ok) {
		log('Failed to get the subscription token:', tok.msg || tok.error);
		save_state({ ok: false, error: tok.error || 'subscription_token', msg: tok.msg });
		return 1;
	}
	const token = readSession().sub_token;
	const ep = subscribeEndpoint();

	/* Countries */
	const loc = request('GET', ep.path + '/location', { base: ep.base, auth: false, timeout: 20, proxy, query: { token } });
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
	if (main_node !== 'urltest' && !fresh[main_node] && !before[main_node]?.country) {
		let first = null;
		for (let name in fresh)
			if (!first || int(fresh[name].country_sort) > int(fresh[first].country_sort))
				first = name;
		uci.set(uciconfig, ucimain, 'main_node', first || 'nil');
		changed = true;
		log(first ? `Main node is gone, switching to ${fresh[first].label}.` : 'No node available.');
	}

	uci.commit(uciconfig);

	const rules = sync_rules(ep, proxy);

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

	if ((changed || rules.changed) && uci.get(uciconfig, ucimain, 'enabled') === '1') {
		log('Restarting service...');
		system('/etc/init.d/hyper restart >/dev/null 2>&1');
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
