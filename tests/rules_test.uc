import { eq, ok, done } from 'lib';
import { looksLikeURL, normalizeCatalog, parseRuleList, toRuleSetSources } from 'rules';

/* catalog validation and ordering, as the app does it */
const cat = normalizeCatalog([
	{ key: 'streaming', value: 'https://rules.example.com/stream.list', policy: 'PROXY', enabled: true, sort: 20 },
	{ key: 'cn-direct', value: 'DOMAIN-SUFFIX,baidu.com\nGEOIP,CN', policy: 'direct', enabled: true, sort: 10 },
	{ key: 'off', value: 'DOMAIN,x.example', policy: 'PROXY', enabled: false, sort: 30 },
	{ key: 'cn-direct', value: 'DOMAIN,dup.example', policy: 'DIRECT', sort: 1 },
	{ key: 'bad policy', value: 'x', policy: 'REJECT' },
	{ key: 'reject', value: 'DOMAIN,ads.example', policy: 'REJECT', sort: 1 },
	{ key: 'empty', value: '  ', policy: 'PROXY' }
]);
eq('order by sort', map(cat.entries, (e) => e.key), ['cn-direct', 'streaming', 'off']);
eq('policy normalised', cat.entries[0].policy, 'DIRECT');
eq('url vs inline', [cat.entries[1].url, cat.entries[0].url == null], ['https://rules.example.com/stream.list', true]);
eq('disabled kept but flagged', cat.entries[2].enabled, false);
eq('errors reported', length(cat.errors), 4);
eq('not a list', normalizeCatalog(null).entries, []);

ok('url', looksLikeURL('https://a.example/x.list'));
ok('not url (multi line)', !looksLikeURL('https://a.example\nDOMAIN,x'));
ok('not url (rule text)', !looksLikeURL('DOMAIN-SUFFIX,a.example'));

/* classical lines */
let p = parseRuleList(join('\n', [
	'# comment',
	'DOMAIN,Exact.Example',
	'DOMAIN-SUFFIX,.Example.COM',
	'DOMAIN-KEYWORD,Tracker',
	'DOMAIN-REGEX,^api[0-9]+\\.example$',
	'IP-CIDR,192.0.2.0/24,no-resolve',
	'IP-CIDR6,2001:db8::/32',
	'GEOIP,CN',
	'PROCESS-NAME,curl',
	'MATCH,DIRECT',
	'IP-CIDR,not-a-cidr'
]));
eq('domain', p.rule.domain, ['exact.example']);
eq('suffix (leading dot dropped, lower-cased)', p.rule.domain_suffix, ['example.com']);
eq('keyword', p.rule.domain_keyword, ['tracker']);
eq('regex kept verbatim', p.rule.domain_regex, ['^api[0-9]+\\.example$']);
eq('cidr v4 + v6', p.rule.ip_cidr, ['192.0.2.0/24', '2001:db8::/32']);
eq('geoip', p.geoip, ['cn']);
eq('counts', [p.count, p.skipped], [7, 3]);

/* YAML payload / plain domain list */
p = parseRuleList("payload:\n  - '+.google.com'\n  - \".youtube.com\"\n  - 'DOMAIN-SUFFIX,ggpht.com'\n  - github.com\n  - 10.0.0.0/8\n  - not a domain\n");
eq('payload suffixes', p.rule.domain_suffix, ['google.com', 'youtube.com', 'ggpht.com']);
eq('payload domain', p.rule.domain, ['github.com']);
eq('payload cidr', p.rule.ip_cidr, ['10.0.0.0/8']);
eq('payload skipped', p.skipped, 1);

/* split into sing-box sources */
let src = toRuleSetSources(parseRuleList('DOMAIN-SUFFIX,a.example\nIP-CIDR,198.51.100.0/24'));
eq('domain source', src.domain, { version: 3, rules: [ { domain_suffix: ['a.example'] } ] });
eq('ip source', src.ip, { version: 3, rules: [ { ip_cidr: ['198.51.100.0/24'] } ] });
src = toRuleSetSources(parseRuleList('GEOIP,CN'));
eq('geoip only -> no files', [src.domain, src.ip], [null, null]);
eq('dedupe', parseRuleList('DOMAIN,a.example\nDOMAIN,A.example').rule.domain, ['a.example']);

done('rules');
