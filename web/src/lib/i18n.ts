import { en } from "./locales/en.ts";

export type MessageKey = keyof typeof en;
export type Locale = "zh-CN" | "en-US";
export type LanguagePreference = "auto" | Locale;
const KEY = "hyper:language";

export function resolveLocale(preference: LanguagePreference, routerLanguage: string, browserLanguage = "zh-CN"): Locale {
  const language = preference === "auto" ? routerLanguage || browserLanguage : preference;
  return /^zh(?:[-_]|$)/i.test(language) ? "zh-CN" : "en-US";
}

function readPreference(): LanguagePreference {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === "zh-CN" || saved === "en-US") return saved;
  } catch { /* disabled storage */ }
  return "auto";
}

function routerLanguage(): string {
  if (typeof window === "undefined") return "";
  try {
    if (window.parent !== window) return window.parent.document.documentElement.lang || new URLSearchParams(location.search).get("lang") || "en";
  } catch { /* iframe language is also passed explicitly by LuCI */ }
  return new URLSearchParams(location.search).get("lang") || "";
}

export const languagePreference = readPreference();
export const locale = resolveLocale(languagePreference, routerLanguage(), typeof window === "undefined" ? "zh-CN" : navigator.language);

export function saveLanguage(preference: LanguagePreference): void {
  localStorage.setItem(KEY, preference);
}

/** Chinese source messages are stable keys; interpolation keeps each sentence whole. */
export function t(message: MessageKey, values: unknown[] = []): string {
  const translated = locale === "en-US" ? en[message] : message;
  return translated.replace(/\{(\d+)\}/g, (match, index) => values[Number(index)] == null ? match : String(values[Number(index)]));
}
