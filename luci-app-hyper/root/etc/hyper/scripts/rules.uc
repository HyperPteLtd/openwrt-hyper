/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Server routing rules (GET <api_base>/api/subscribe/rules) -> sing-box rule sets.
 *
 * Each entry is { key, value, policy: DIRECT|PROXY, enabled, sort } (same as the
 * Hyper app, hyper-tunnel crates/runtime/src/rule_resource_store/catalog.rs).
 * `value` is either an http(s) URL of a rule list or the list itself. Lists are
 * Clash style, classical ("DOMAIN-SUFFIX,example.com") or plain domain / CIDR
 * lines, optionally inside a YAML "payload:" block.
 *
 * Pure functions: unit-tested on a PC (tests/rules_test.uc).
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

export const MAX_RULE_ENTRIES = 128;
export const POLICIES = { DIRECT: 'direct-out', PROXY: 'main-out' };

export function isValidKey(key) {
	return type(key) === 'string' && match(key, /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/) != null;
};

export function looksLikeURL(value) {
	return type(value) === 'string' && index(value, '\n') === -1 && match(trim(value), /^https?:\/\/[^[:space:]]+$/) != null;
};

/* Validate and order the catalog. Returns { entries, errors } */
export function normalizeCatalog(list) {
	const entries = [], errors = [], seen = {};
	if (type(list) !== 'array')
		return { entries, errors: [ 'not a list' ] };

	for (let e in slice(list, 0, MAX_RULE_ENTRIES)) {
		if (type(e) !== 'object' || !isValidKey(e.key)) {
			push(errors, `invalid key: ${e?.key}`);
			continue;
		}
		if (seen[e.key]) {
			push(errors, `duplicate key: ${e.key}`);
			continue;
		}
		const policy = uc(`${e.policy || ''}`);
		if (!POLICIES[policy]) {
			push(errors, `unsupported policy for ${e.key}: ${e.policy}`);
			continue;
		}
		if (type(e.value) !== 'string' || trim(e.value) === '') {
			push(errors, `empty value for ${e.key}`);
			continue;
		}
		seen[e.key] = true;
		push(entries, {
			key: e.key,
			policy,
			enabled: e.enabled !== false,
			sort: (type(e.sort) === 'int') ? e.sort : 0,
			url: looksLikeURL(e.value) ? trim(e.value) : null,
			inline: looksLikeURL(e.value) ? null : e.value
		});
	}

	return { entries: sort(entries, (a, b) => (a.sort - b.sort) || ((a.key < b.key) ? -1 : 1)), errors };
};

function isCIDR(v) {
	return match(v, /^[0-9.]+\/[0-9]{1,2}$/) != null || match(v, /^[0-9A-Fa-f:]+\/[0-9]{1,3}$/) != null;
}

function isIP(v) {
	return match(v, /^[0-9]{1,3}(\.[0-9]{1,3}){3}$/) != null || (index(v, ':') !== -1 && match(v, /^[0-9A-Fa-f:]+$/) != null);
}

function isDomain(v) {
	return match(v, /^[A-Za-z0-9_*]([A-Za-z0-9_.-]*[A-Za-z0-9_])?$/) != null && index(v, '.') !== -1;
}

/*
 * Rule list text -> { rule: <sing-box headless rule>, geoip: [codes], count, skipped }.
 * GEOIP entries are returned separately (the generator maps CN to the bundled set).
 */
export function parseRuleList(text) {
	const rule = { domain: [], domain_suffix: [], domain_keyword: [], domain_regex: [], ip_cidr: [] };
	const geoip = [];
	let count = 0, skipped = 0;

	for (let line in split(replace(text || '', /\r/g, ''), '\n')) {
		line = trim(line);
		if (line === '' || substr(line, 0, 1) === '#' || substr(line, 0, 2) === '//' || line === 'payload:')
			continue;

		/* YAML list item: - 'DOMAIN-SUFFIX,x' / - "+.x" / - x */
		if (substr(line, 0, 1) === '-')
			line = trim(substr(line, 1));
		line = trim(replace(line, /^['"]|['"]$/g, ''));
		if (line === '')
			continue;

		if (index(line, ',') !== -1) {
			const f = map(split(line, ','), (x) => trim(x));
			const kind = uc(f[0]), v = f[1];
			if (v == null || v === '') {
				skipped++;
				continue;
			}
			switch (kind) {
			case 'DOMAIN':
				push(rule.domain, lc(v));
				break;
			case 'DOMAIN-SUFFIX':
				push(rule.domain_suffix, lc(replace(v, /^\.+/, '')));
				break;
			case 'DOMAIN-KEYWORD':
				push(rule.domain_keyword, lc(v));
				break;
			case 'DOMAIN-REGEX':
				push(rule.domain_regex, v);
				break;
			case 'IP-CIDR':
			case 'IP-CIDR6':
				if (!isCIDR(v) && !isIP(v)) {
					skipped++;
					continue;
				}
				push(rule.ip_cidr, v);
				break;
			case 'GEOIP':
				push(geoip, lc(v));
				break;
			default:
				/* PROCESS-NAME, MATCH, ... have no meaning on a router */
				skipped++;
				continue;
			}
			count++;
			continue;
		}

		/* plain list line */
		if (isCIDR(line))
			push(rule.ip_cidr, line);
		else if (substr(line, 0, 2) === '+.')
			push(rule.domain_suffix, lc(substr(line, 2)));
		else if (substr(line, 0, 1) === '.')
			push(rule.domain_suffix, lc(substr(line, 1)));
		else if (isDomain(line))
			push(rule.domain, lc(line));
		else {
			skipped++;
			continue;
		}
		count++;
	}

	for (let k in keys(rule)) {
		rule[k] = uniq(rule[k]);
		if (!length(rule[k]))
			delete rule[k];
	}

	return { rule, geoip: uniq(geoip), count, skipped };
};

/*
 * sing-box source rule-set files (version 3 = sing-box 1.11+), split by kind:
 * domain rules can steer DNS, IP rules only routing (IP rules inside DNS rules
 * would be matched against responses instead). Either may be null.
 */
export function toRuleSetSources(parsed) {
	const domain = {}, ip = {};
	for (let k in [ 'domain', 'domain_suffix', 'domain_keyword', 'domain_regex' ])
		if (parsed.rule[k])
			domain[k] = parsed.rule[k];
	if (parsed.rule.ip_cidr)
		ip.ip_cidr = parsed.rule.ip_cidr;

	return {
		domain: length(keys(domain)) ? { version: 3, rules: [ domain ] } : null,
		ip: length(keys(ip)) ? { version: 3, rules: [ ip ] } : null
	};
};
