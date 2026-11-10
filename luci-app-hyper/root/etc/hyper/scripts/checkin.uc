/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Daily check-in, kept on the router only.
 *
 * PPanel has no check-in API. The Hyper app's check-in calendar is stored on
 * the phone in the same way: a list of local dates, no reward attached. Here
 * the list is per account, so a family router shared by two accounts keeps
 * two calendars.
 *
 * /etc/hyper/checkin.json
 *   { "version": 1, "accounts": { "<md5(email)[0..16]>": [ "2026-11-01", ... ] } }
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

'use strict';

import { md5 } from 'md5';	/* plain-ucode MD5: 23.05 has no ucode-mod-digest */
import { readfile, rename, writefile } from 'fs';

export const CHECKIN_FILE = '/etc/hyper/checkin.json';
export const MAX_DATES = 1100;		/* about three years per account */
export const MAX_ACCOUNTS = 16;

/* 2025-01-01. Until NTP syncs, a router without an RTC boots with an old clock. */
const SANE_CLOCK = 1735689600;

const DAYS_IN_MONTH = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];

/* A real calendar date written as YYYY-MM-DD */
export function isValidDate(s) {
	/* the length check also stops "$" from accepting a trailing newline */
	if (type(s) !== 'string' || length(s) !== 10)
		return false;
	const m = match(s, /^([0-9]{4})-([0-9]{2})-([0-9]{2})$/);
	if (!m)
		return false;

	const y = int(m[1]), mo = int(m[2]), d = int(m[3]);
	if (y < 2000 || mo < 1 || mo > 12 || d < 1)
		return false;
	const leap = (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0;
	return d <= ((mo === 2 && leap) ? 29 : DAYS_IN_MONTH[mo - 1]);
};

/* Days since 1970-01-01 for a valid date (H. Hinnant's days_from_civil, years >= 2000) */
export function dayNumber(s) {
	const m = int(substr(s, 5, 2)), d = int(substr(s, 8, 2));
	const y = int(substr(s, 0, 4)) - ((m <= 2) ? 1 : 0);
	const era = int(y / 400);
	const yoe = y - era * 400;
	const doy = int((153 * (m + ((m > 2) ? -3 : 9)) + 2) / 5) + d - 1;
	const doe = yoe * 365 + int(yoe / 4) - int(yoe / 100) + doy;
	return era * 146097 + doe - 719468;
};

/*
 * The browser sends its own local date, like the app uses the phone's date.
 * Once the router clock is set, refuse dates more than a day away from it
 * (time zones span at most one day either side of UTC).
 */
export function isPlausibleDate(date, now) {
	if (!isValidDate(date))
		return false;
	if (now < SANE_CLOCK)
		return true;
	const diff = dayNumber(date) - int(now / 86400);
	return diff >= -1 && diff <= 1;
};

/* Stable, non-reversible key for the signed-in account */
export function accountKey(email) {
	const e = lc(trim(`${email ?? ''}`));
	if (e === '')
		return null;
	const h = md5(e);
	return h ? substr(h, 0, 16) : null;
};

function cleanDates(list) {
	const seen = {};
	for (let d in ((type(list) === 'array') ? list : []))
		if (isValidDate(d))
			seen[d] = true;
	return slice(sort(keys(seen)), -MAX_DATES);
}

export function normalize(store) {
	const accounts = {};
	const src = store?.accounts;
	if (type(src) === 'object')
		for (let k, v in src) {
			if (!match(k, /^[0-9a-f]{16}$/))
				continue;
			const dates = cleanDates(v);
			if (length(dates))
				accounts[k] = dates;
		}
	return { version: 1, accounts };
};

export function datesFor(store, key) {
	return (key && normalize(store).accounts[key]) || [];
};

/*
 * Check `key` in on `date`.
 * Returns { store, dates, added } (added false when already checked in that day) or { error }.
 */
export function record(store, key, date, now) {
	if (!key)
		return { error: 'not signed in' };
	if (!isPlausibleDate(date, now))
		return { error: 'illegal date' };

	const s = normalize(store);
	const dates = s.accounts[key] || [];
	if (date in dates)
		return { store: s, dates, added: false };

	s.accounts[key] = cleanDates([ ...dates, date ]);

	/* keep the accounts that checked in most recently */
	const ks = keys(s.accounts);
	if (length(ks) > MAX_ACCOUNTS) {
		const last = (k) => s.accounts[k][length(s.accounts[k]) - 1];
		const order = sort(filter(ks, (k) => k !== key), (a, b) => (last(a) < last(b)) ? -1 : (last(a) > last(b)) ? 1 : 0);
		for (let k in slice(order, 0, length(ks) - MAX_ACCOUNTS))
			delete s.accounts[k];
	}

	return { store: s, dates: s.accounts[key], added: true };
};

export function load(path) {
	try {
		return normalize(json(readfile(path || CHECKIN_FILE) || 'null'));
	} catch (e) {
		return normalize(null);
	}
};

export function save(store, path) {
	path = path || CHECKIN_FILE;
	const tmp = path + '.tmp';
	if (writefile(tmp, sprintf('%J\n', normalize(store))) == null)
		return false;
	return !!rename(tmp, path);
};
