// SPDX-License-Identifier: GPL-2.0-only

/* Both sources use the router's local clock. Keep continuation lines attached
 * to their preceding record while interleaving the two timestamp formats. */
export function mergeLogs(sources, limit) {
	const records = [];
	let sequence = 0;
	for (let source in sources) {
		let current = null;
		for (let line in split(source || '', '\n')) {
			if (!length(trim(line))) continue;
			const stamp = match(line, /(^|\s)(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})/);
			if (!stamp && current) {
				current.text += '\n' + line;
				continue;
			}
			current = { time: stamp ? stamp[2] + ' ' + stamp[3] : '', order: sequence++, text: line };
			push(records, current);
		}
	}
	sort(records, (a, b) => a.time < b.time ? -1 : a.time > b.time ? 1 : a.order - b.order);
	return join('\n', map(slice(records, -limit), (r) => r.text));
};
