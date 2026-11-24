// The pages' language. Their text is written in Chinese, which is also the key of its English
// in en.js: t('当前 {0}', version) reads "Current {0}" in English. The choice (界面语言) is this
// browser's: it follows LuCI's language unless set, and a change reloads the page.
import { en } from './en.js';

const KEY = 'hyper:language';

// The saved choice: auto, zh-CN or en-US.
export function languagePreference() {
	try {
		const v = localStorage.getItem(KEY);
		if (v === 'zh-CN' || v === 'en-US') return v;
	} catch (e) {}
	return 'auto';
}

export function setLanguagePreference(value) {
	try {
		if (value === 'zh-CN' || value === 'en-US') localStorage.setItem(KEY, value);
		else localStorage.removeItem(KEY);
	} catch (e) {}
	location.reload();
}

function detect() {
	const pref = languagePreference();
	if (pref !== 'auto') return pref;
	// LuCI writes the language it renders in on the page; without it, the browser's.
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
