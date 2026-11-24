// The pages' language, LuCI's own. Their text is written in Chinese, which is also the key of
// its English in en.js: t('当前 {0}', version) reads "Current {0}" in English.
import { en } from './en.js';

// LuCI's language, which LuCI writes on the page; without it, the browser's.
function detect() {
	const lang = (document.documentElement.lang || navigator.language || 'zh').toLowerCase();
	return lang.startsWith('zh') ? 'zh-CN' : 'en-US';
}

// zh-CN or en-US, for the page, dates and numbers, and the Hyper service's own text.
export const locale = typeof document === 'undefined' ? 'zh-CN' : detect();
const english = locale === 'en-US';

export function t(text, ...args) {
	const s = english && Object.hasOwn(en, text) ? en[text] : text;
	if (!args.length) return s;
	return s.replace(/\{\{|\}\}|\{(\d+)\}/g, (m, i) => (i === undefined ? m[0] : String(args[Number(i)] ?? '')));
}
