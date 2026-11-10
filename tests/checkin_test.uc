import { eq, ok, done } from 'lib';
import { readfile, unlink } from 'fs';
import {
	accountKey, datesFor, dayNumber, isPlausibleDate, isValidDate, load, normalize, record, save,
	MAX_ACCOUNTS, MAX_DATES
} from 'checkin';

/* 2026-11-09 12:00:00 UTC */
const NOW = 1794225600;

/* dates */
for (let d in ['2026-11-09', '2024-02-29', '2000-02-29', '2026-12-31', '2026-01-01'])
	ok(`valid ${d}`, isValidDate(d));
for (let d in ['2026-02-29', '1900-02-29', '2026-13-01', '2026-00-10', '2026-04-31', '2026-11-9',
		'2026/11/09', ' 2026-11-09', '2026-11-09\n', '1999-12-31', '', null, 20261109])
	ok(`invalid ${d}`, !isValidDate(d));

eq('dayNumber epoch+', dayNumber('2000-01-01'), 10957);
eq('dayNumber leap day', dayNumber('2024-03-01') - dayNumber('2024-02-28'), 2);
eq('dayNumber matches clock', dayNumber('2026-11-09'), int(NOW / 86400));

/* the browser's local date may be a day either side of UTC, no more */
ok('today', isPlausibleDate('2026-11-09', NOW));
ok('yesterday (UTC-x)', isPlausibleDate('2026-11-08', NOW));
ok('tomorrow (UTC+x)', isPlausibleDate('2026-11-10', NOW));
ok('two days ahead', !isPlausibleDate('2026-11-11', NOW));
ok('last week', !isPlausibleDate('2026-11-02', NOW));
ok('router clock not set yet: any real date', isPlausibleDate('2026-11-02', 1000));
ok('router clock not set yet: still a real date', !isPlausibleDate('2026-02-30', 1000));

/* account keys */
const k1 = accountKey('Astra@Example.com '), k2 = accountKey('bob@example.com');
ok('key is 16 hex', match(k1, /^[0-9a-f]{16}$/));
eq('key ignores case and spaces', accountKey('astra@example.com'), k1);
ok('different accounts differ', k1 !== k2);
eq('no email, no key', accountKey(''), null);
eq('null email, no key', accountKey(null), null);

/* recording */
let r = record(null, k1, '2026-11-09', NOW);
eq('first check-in added', [r.added, r.dates], [true, ['2026-11-09']]);
let s = r.store;
r = record(s, k1, '2026-11-09', NOW);
eq('same day twice', [r.added, r.dates], [false, ['2026-11-09']]);
r = record(s, k1, '2026-11-08', NOW);
eq('dates stay sorted', r.dates, ['2026-11-08', '2026-11-09']);
s = r.store;
r = record(s, k2, '2026-11-09', NOW);
s = r.store;
eq('accounts are separate', [datesFor(s, k1), datesFor(s, k2)], [['2026-11-08', '2026-11-09'], ['2026-11-09']]);
eq('unknown account', datesFor(s, accountKey('nobody@example.com')), []);
eq('no key', datesFor(s, null), []);
eq('not signed in', record(s, null, '2026-11-09', NOW), { error: 'not signed in' });
eq('bad date', record(s, k1, '2026-11-31', NOW), { error: 'illegal date' });
eq('far date', record(s, k1, '2027-01-01', NOW), { error: 'illegal date' });

/* normalize drops junk from a hand-edited or corrupted file */
eq('normalize', normalize({ version: 9, accounts: {
	[k1]: ['2026-11-09', 'x', '2026-11-09', 7, '2026-11-01'],
	'NOT-A-KEY': ['2026-11-09'],
	[k2]: 'oops',
	'0123456789abcdef': []
} }), { version: 1, accounts: { [k1]: ['2026-11-01', '2026-11-09'] } });
eq('normalize null', normalize(null), { version: 1, accounts: {} });
eq('normalize array', normalize([1, 2]), { version: 1, accounts: {} });

/* caps */
let big = { accounts: { [k1]: [] } };
for (let i = 0; i < MAX_DATES + 50; i++) {
	const day = 10957 + i;	/* from 2000-01-01 */
	const t = gmtime(day * 86400);
	push(big.accounts[k1], sprintf('%04d-%02d-%02d', t.year, t.mon, t.mday));
}
r = record(big, k1, '2026-11-09', NOW);
eq('dates capped', length(r.dates), MAX_DATES);
eq('newest kept', r.dates[length(r.dates) - 1], '2026-11-09');

let many = { accounts: {} };
for (let i = 0; i < MAX_ACCOUNTS; i++)
	many.accounts[accountKey(`user${i}@example.com`)] = [sprintf('2026-10-%02d', i + 1)];
r = record(many, k1, '2026-11-09', NOW);
eq('accounts capped', length(r.store.accounts), MAX_ACCOUNTS);
ok('new account kept', k1 in keys(r.store.accounts));
ok('stalest account dropped', !(accountKey('user0@example.com') in keys(r.store.accounts)));
ok('recent account kept', accountKey(`user${MAX_ACCOUNTS - 1}@example.com`) in keys(r.store.accounts));

/* file round trip */
const path = `/tmp/hyper-checkin-test-${time()}.json`;
eq('missing file', load(path), { version: 1, accounts: {} });
ok('save', save(s, path));
eq('load', load(path), s);
ok('no temp file left', readfile(path + '.tmp') == null);
unlink(path);

done('checkin');
