// Test stub for the OpenWrt `uci` module.
// Reads a fixture from $HYPER_UCI_FIXTURE: { "<config>": { "<section>": { ".type": "...", "<option>": ... } } }
// Section order follows the JSON object order.

import { readfile } from 'fs';

let data = null;

function load_fixture() {
	if (data == null) {
		const path = getenv('HYPER_UCI_FIXTURE');
		data = path ? json(readfile(path)) : {};
	}
	return data;
}

export function cursor() {
	const d = load_fixture();

	return {
		load: (conf) => true,
		get: (conf, sec, opt) => {
			const s = d[conf]?.[sec];
			if (!s)
				return null;
			return (opt == null) ? s['.type'] : s[opt];
		},
		get_all: (conf, sec) => {
			const s = d[conf]?.[sec];
			return s ? { '.name': sec, ...s } : null;
		},
		get_first: (conf, type) => {
			for (let name in d[conf] || {})
				if (d[conf][name]['.type'] === type)
					return name;
			return null;
		},
		foreach: (conf, type, cb) => {
			for (let name in d[conf] || {})
				if (type == null || d[conf][name]['.type'] === type)
					cb({ '.name': name, '.type': d[conf][name]['.type'], ...d[conf][name] });
		},
		set: (conf, sec, opt, val) => {
			d[conf] = d[conf] || {};
			if (val == null)
				d[conf][sec] = { '.type': opt };
			else
				d[conf][sec][opt] = val;
			return true;
		},
		delete: (conf, sec, opt) => {
			if (opt == null)
				delete d[conf][sec];
			else
				delete d[conf][sec][opt];
			return true;
		},
		commit: (conf) => true
	};
};
