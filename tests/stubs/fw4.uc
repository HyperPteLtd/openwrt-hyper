// Test stub for fw4 (only what firewall_post.ut uses).
return {
	parse_subnet: (s) => {
		const m = match(s, /^([0-9a-fA-F:.]+)\/([0-9]+)$/);
		return m ? [{ family: (index(m[1], ':') !== -1) ? 6 : 4, addr: m[1], bits: int(m[2]) }] : null;
	},
	parse_mark: (s) => {
		const m = match(`${s}`, /^(0x[0-9a-fA-F]+|[0-9]+)(\/(0x[0-9a-fA-F]+|[0-9]+))?$/);
		const num = (v) => (substr(v, 0, 2) === '0x') ? hex(v) : int(v);
		return m ? { mark: num(m[1]), mask: m[3] ? num(m[3]) : 0xffffffff } : null;
	},
	hex: (n) => sprintf('0x%x', n)
};
