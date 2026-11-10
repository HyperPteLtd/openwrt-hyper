/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Copyright (C) 2023 ImmortalWrt.org
 * Copyright (C) 2026 Hyper Pte. Ltd. (session + curl helpers)
 */

import { mkstemp, readfile, rename, writefile } from 'fs';
import { urldecode_params } from 'luci.http';

/* Global variables start */
export const HP_DIR = '/etc/hyper';
export const RUN_DIR = '/var/run/hyper';
/* Global variables end */

/* Utilities start */
/* Kanged from luci-app-commands */
export function shellQuote(s) {
	return `'${replace(s, "'", "'\\''")}'`;
};

/*
 * Any control character, NUL included. Regexes stop at a NUL byte, so it is
 * looked up separately: "ok\u0000\nrest" would otherwise hide its line break.
 */
export function hasControlChar(s) {
	s = `${s}`;
	return index(s, chr(0)) !== -1 || match(s, /[[:cntrl:]]/) != null;
};

/*
 * ucode compiles regexes with REG_NEWLINE, so ^ and $ also match at line
 * breaks: match("ok\nanything", /^ok$/) succeeds. Validators of whole values
 * must go through this, which refuses every control character first.
 */
export function matchWhole(s, re) {
	return type(s) === 'string' && !hasControlChar(s) && match(s, re) != null;
};

export function isBinary(str) {
	for (let off = 0, byte = ord(str); off < length(str); byte = ord(str, ++off))
		if (byte <= 8 || (byte >= 14 && byte <= 31))
			return true;

	return false;
};

export function executeCommand(...args) {
	let outfd = mkstemp();
	let errfd = mkstemp();

	/* /dev/fd/N, not >&N: dash (Debian /bin/sh, used by the PC tests) only takes fds 0-9 */
	const exitcode = system(`${join(' ', args)} >/dev/fd/${outfd.fileno()} 2>/dev/fd/${errfd.fileno()}`);

	outfd.seek(0);
	errfd.seek(0);

	const stdout = outfd.read(1024 * 512) ?? '';
	const stderr = errfd.read(1024 * 512) ?? '';

	outfd.close();
	errfd.close();

	const binary = isBinary(stdout);

	return {
		command: join(' ', args),
		stdout: binary ? null : stdout,
		stderr,
		exitcode,
		binary
	};
};

export function getTime(epoch) {
	const local_time = localtime(epoch);
	return replace(replace(sprintf(
		'%d-%2d-%2d@%2d:%2d:%2d',
		local_time.year,
		local_time.mon,
		local_time.mday,
		local_time.hour,
		local_time.min,
		local_time.sec
	), ' ', '0'), '@', ' ');

};

export function wGET(url, ua) {
	if (!url || type(url) !== 'string')
		return null;

	if (!ua)
		ua = 'luci-app-hyper (OpenWrt)';

	const output = executeCommand(`/usr/bin/wget -qO- --user-agent ${shellQuote(ua)} --timeout=10 ${shellQuote(url)}`) || {};
	return trim(output.stdout);
};
/* Utilities end */

/* String helper start */
export function isEmpty(res) {
	return !res || res === 'nil' || (type(res) in ['array', 'object'] && length(res) === 0);
};

export function strToBool(str) {
	return (str === '1') || null;
};

export function strToInt(str) {
	return !isEmpty(str) ? (int(str) || null) : null;
};

export function strToTime(str) {
	return !isEmpty(str) ? (str + 's') : null;
};

function versionPart(x, y) {
	const nx = match(x, /^[0-9]+$/), ny = match(y, /^[0-9]+$/);
	if (nx && ny) {
		x = int(x);
		y = int(y);
	} else if (nx || ny) {
		return nx ? -1 : 1;	/* numeric identifiers sort before words (semver) */
	}
	return (x < y) ? -1 : (x > y) ? 1 : 0;
}

/*
 * Compare package versions: -1, 0 or 1. A leading "v" (release tags) and
 * OpenWrt's "-r<PKG_RELEASE>" suffix are ignored, so the installed "0.2.0-r1"
 * equals the release tag "v0.2.0". A pre-release ("0.2.0-beta.1") sorts
 * before its release; missing parts count as 0 ("1.2" equals "1.2.0").
 */
export function versionCompare(a, b) {
	const parse = (v) => {
		v = replace(replace(trim(`${v ?? ''}`), /^v/, ''), /-r[0-9]+$/, '');
		const dash = index(v, '-');
		return {
			main: (v === '') ? [] : split((dash >= 0) ? substr(v, 0, dash) : v, '.'),
			pre: (dash >= 0) ? split(substr(v, dash + 1), '.') : []
		};
	};
	const x = parse(a), y = parse(b);

	const n = (length(x.main) > length(y.main)) ? length(x.main) : length(y.main);
	for (let i = 0; i < n; i++) {
		const c = versionPart(x.main[i] ?? '0', y.main[i] ?? '0');
		if (c)
			return c;
	}

	if (!length(x.pre) || !length(y.pre))
		return length(x.pre) ? -1 : length(y.pre) ? 1 : 0;
	const m = (length(x.pre) > length(y.pre)) ? length(x.pre) : length(y.pre);
	for (let i = 0; i < m; i++) {
		if (x.pre[i] == null || y.pre[i] == null)
			return (x.pre[i] == null) ? -1 : 1;
		const c = versionPart(x.pre[i], y.pre[i]);
		if (c)
			return c;
	}
	return 0;
};

export function removeBlankAttrs(res) {
	let content;

	if (type(res) === 'object') {
		content = {};
		map(keys(res), (k) => {
			if (type(res[k]) in ['array', 'object'])
				content[k] = removeBlankAttrs(res[k]);
			else if (res[k] !== null && res[k] !== '')
				content[k] = res[k];
		});
	} else if (type(res) === 'array') {
		content = [];
		map(res, (k, i) => {
			if (type(k) in ['array', 'object'])
				push(content, removeBlankAttrs(k));
			else if (k !== null && k !== '')
				push(content, k);
		});
	} else
		return res;

	return content;
};

export function validateHostname(hostname) {
	return (match(hostname, /^[a-zA-Z0-9_]+$/) != null ||
		(match(hostname, /^[a-zA-Z0-9_][a-zA-Z0-9_%-.]*[a-zA-Z0-9]$/) &&
			match(hostname, /[^0-9.]/)));
};

export function validation(datatype, data) {
	if (!datatype || !data)
		return null;

	const ret = system(`/sbin/validate_data ${shellQuote(datatype)} ${shellQuote(data)} 2>/dev/null`);
	return (ret === 0);
};
/* String helper end */

/* String parser start */
export function decodeBase64Str(str) {
	if (isEmpty(str))
		return null;

	str = trim(str);
	str = replace(str, '_', '/');
	str = replace(str, '-', '+');

	const padding = length(str) % 4;
	if (padding)
		str = str + substr('====', padding);

	return b64dec(str);
};

export function parseURL(url) {
	if (type(url) !== 'string')
		return null;

	const services = {
		http: '80',
		https: '443'
	};

	const objurl = {};

	objurl.href = url;

	url = replace(url, /#(.+)$/, (_, val) => {
		objurl.hash = val;
		return '';
	});

	url = replace(url, /^(\w[A-Za-z0-9\+\-\.]+):/, (_, val) => {
		objurl.protocol = val;
		return '';
	});

	url = replace(url, /\?(.+)/, (_, val) => {
		objurl.search = val;
		objurl.searchParams = urldecode_params(val);
		return '';
	});

	url = replace(url, /^\/\/([^\/]+)/, (_, val) => {
		val = replace(val, /^([^@]+)@/, (_, val) => {
			objurl.userinfo = val;
			return '';
		});

		val = replace(val, /:(\d+)$/, (_, val) => {
			objurl.port = val;
			return '';
		});

		if (validation('ip4addr', val) ||
		    validation('ip6addr', replace(val, /\[|\]/g, '')) ||
		    validation('hostname', val))
			objurl.hostname = val;

		return '';
	});

	objurl.pathname = url || '/';

	if (!objurl.protocol || !objurl.hostname)
		return null;

	if (objurl.userinfo) {
		objurl.userinfo = replace(objurl.userinfo, /:(.+)$/, (_, val) => {
			objurl.password = val;
			return '';
		});

		if (match(objurl.userinfo, /^([A-Za-z0-9\+\-\_\.]|%[A-Za-z0-9]{2})+$/)) {
			objurl.username = objurl.userinfo;
			delete objurl.userinfo;
		} else {
			delete objurl.userinfo;
			delete objurl.password;
		}
	};

	if (!objurl.port)
		objurl.port = services[objurl.protocol];

	objurl.host = objurl.hostname + (objurl.port ? `:${objurl.port}` : '');
	objurl.origin = `${objurl.protocol}://${objurl.host}`;

	return objurl;
};
/* String parser end */

/* Hyper start */
export const SESSION_FILE = HP_DIR + '/session';
export const SYNC_STATE_FILE = RUN_DIR + '/sync.json';
/* Programs bundled with the package (scripts/stage-binaries.sh); a system sing-box, if any, is not used */
export const BIN_DIR = '/usr/libexec/hyper/bin';
export const SINGBOX_BIN = BIN_DIR + '/sing-box';	/* libbox core, pinned in scripts/core.lock */
export const CRYPT_BIN = BIN_DIR + '/hyper-crypt';

/* PPanel session: JWT and subscription token. Root-only (0600), never sent to the browser. */
export function readSession() {
	try {
		return json(readfile(SESSION_FILE) || 'null');
	} catch (e) {
		return null;
	}
};

export function writeSession(session) {
	const tmp = SESSION_FILE + '.tmp';

	system(`mkdir -p ${HP_DIR}; umask 077; : > ${tmp}`);
	writefile(tmp, sprintf('%J\n', session));
	system(`chmod 600 ${tmp}`);
	return rename(tmp, SESSION_FILE);
};

export function clearSession() {
	system(`rm -f ${SESSION_FILE}`);
};

function curlQuote(s) {
	return '"' + replace(replace(`${s}`, '\\', '\\\\'), '"', '\\"') + '"';
}

/*
 * HTTP via curl. URL, headers (JWT / token) and body go through a curl config
 * file and stdin on unlinked temp files, so secrets never show up in argv.
 * `proxy` (optional) is a curl proxy URL such as socks5h://127.0.0.1:5330.
 * Returns { status, body, error }.
 */
export function httpRequest(method, url, headers, body, timeout, proxy) {
	/* one curl config line per value: a line break would start a new directive */
	const parts = [ url, method || 'GET', proxy || '' ];
	for (let k in headers)
		if (headers[k] != null)
			push(parts, k, headers[k]);
	for (let p in parts)
		if (hasControlChar(p))
			return { status: 0, body: '', error: 'illegal character in request' };

	const conf = mkstemp(), input = mkstemp(), output = mkstemp(),
	      status = mkstemp(), errout = mkstemp();

	const lines = [
		`url = ${curlQuote(url)}`,
		`request = ${curlQuote(method || 'GET')}`,
		'silent',
		'show-error',
		'location',
		`max-time = ${int(timeout) || 15}`,
		`output = "/dev/fd/${output.fileno()}"`,
		'write-out = "%{http_code}"'
	];
	if (proxy)
		push(lines, `proxy = ${curlQuote(proxy)}`);
	for (let k in headers)
		if (headers[k] != null)
			push(lines, `header = ${curlQuote(k + ': ' + headers[k])}`);

	if (body != null) {
		input.write((type(body) === 'string') ? body : sprintf('%J', body));
		input.seek(0);
		push(lines, 'data-binary = "@-"');
	}

	conf.write(join('\n', lines) + '\n');
	conf.seek(0);

	const exitcode = system(`/usr/bin/curl -K /dev/fd/${conf.fileno()} </dev/fd/${input.fileno()} >/dev/fd/${status.fileno()} 2>/dev/fd/${errout.fileno()}`);

	output.seek(0);
	status.seek(0);
	errout.seek(0);

	const res = {
		status: int(trim(status.read('all') || '0')),
		body: output.read('all') ?? '',
		error: (exitcode !== 0) ? (trim(errout.read('all') || '') || `curl exited with ${exitcode}`) : null
	};

	for (let fd in [conf, input, output, status, errout])
		fd.close();

	return res;
};
/* Hyper end */
