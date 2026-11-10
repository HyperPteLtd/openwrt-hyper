/*
 * Turn tests/fixtures/links.txt (one share link of every protocol) into a UCI
 * fixture: the nodes of uci-basic.json are replaced by what snapshot.uc parses.
 *   ucode tests/links_fixture.uc <links.txt> <uci-basic.json> <out.json>
 */
import { readfile, writefile } from 'fs';
import { parseSubscription } from 'snapshot';

const links = readfile(ARGV[0]);
const base = json(readfile(ARGV[1]));
const r = parseSubscription(links, { country: 'JP' });

for (let name in keys(base.hyper))
	if (base.hyper[name]['.type'] === 'node')
		delete base.hyper[name];

let i = 0, first = null;
for (let n in r.nodes) {
	const name = sprintf('pp_l%02d', i++);
	base.hyper[name] = { '.type': 'node', ...n };
	first = first || name;
}
base.hyper.config.main_node = first;
writefile(ARGV[2], sprintf('%J\n', base));
printf('%J\n', { nodes: length(r.nodes), types: map(r.nodes, (n) => n.type), invalid: r.invalid, unsupported: r.unsupported });
