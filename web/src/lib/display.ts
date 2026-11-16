import { locale, t as tr, type Locale } from "./i18n.ts";

/** Backend labels can be plain text, a locale map, or a JSON-encoded map. */
export function localizedText(value: unknown, fallback = "", language: Locale = locale): string {
  if (typeof value === "string") {
    const text = value.trim();
    if (!text.startsWith("{")) return text || fallback;
    try { value = JSON.parse(text); } catch { return fallback; }
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) return fallback;
  const entries = Object.entries(value);
  const chinese = ["zh-CN", "zh_Hans", "zh-Hans", "zh", "cn"];
  for (const key of language === "zh-CN" ? [...chinese, "en-US", "en"] : ["en-US", "en", ...chinese]) {
    const text = entries.find(([k]) => k.toLowerCase() === key.toLowerCase())?.[1];
    if (typeof text === "string" && text.trim()) return text.trim();
  }
  return entries.map(([, text]) => text).find((text): text is string => typeof text === "string" && !!text.trim())?.trim() || fallback;
}

/** Product terminology for diagnostic output; the underlying logs stay intact. */
export function serviceText(text: string): string {
  return text.replace(/sing[-_]?box(?:-c)?/gi, tr("代理服务"))
    .replace(/no_subscription/g, tr("尚未开通套餐"))
    .replace(/subscription_token/g, tr("节点访问凭证"))
    .replace(/subscriptions?/gi, tr("节点配置"))
    .replace(/同步订阅/g, tr("更新节点"))
    .replace(/\b(?:anytls|vless|vmess|trojan|shadowsocks|hysteria2?|tuic|wireguard|reality)\b/gi, tr("连接"));
}

/** PPanel uses null/zero (and old Go zero dates) for unlimited validity. */
export function expiryText(value: unknown, status = 1, now = Date.now()): string {
  const n = typeof value === "number" ? value : typeof value === "string" && value.trim() ? Number(value) : NaN;
  const suffix = status === 1 ? "" : tr(" · 已失效");
  if (value === null || n === 0 || n === -62135596800000 || n === -62135596800) return tr("∞ 无限期") + suffix;
  if (!Number.isFinite(n) || n < 0) return tr("到期时间未知") + suffix;
  const ms = n > 1e12 ? n : n * 1000;
  const d = new Date(ms);
  if (!Number.isFinite(d.getTime())) return tr("到期时间未知") + suffix;
  const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  if (ms <= now) return tr("{0} 到期 · 已过期", [date]);
  return status === 1 ? tr("{0} 到期 · 剩余 {1} 天", [date, Math.ceil((ms - now) / 86400000)]) : tr("{0} 到期", [date]) + suffix;
}
