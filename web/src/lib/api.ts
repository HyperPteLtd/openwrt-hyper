import { t as tr } from "./i18n";
/** Hyper service calls through luci.hyper `api` (allowlisted paths, token added on the router). */
import { call } from "./rpc";
import type { ReadOptions } from "./cache";
import { locale } from "./i18n";
import { DEFAULT_SITE_CURRENCY, formatMinorMoney, MINOR_AMOUNT_SCALE, type CurrencyDisplay } from "./currency";

export interface ApiResult<T> {
  ok: boolean;
  status?: number;
  code?: number | null;
  msg?: string | null;
  data?: T;
  error?: string;
}

export async function api<T>(
  method: "GET" | "POST" | "PUT" | "DELETE",
  path: string,
  opts: { query?: Record<string, unknown>; body?: unknown; idempotencyKey?: string } & ReadOptions<ApiResult<T>> = {},
): Promise<ApiResult<T>> {
  return call<ApiResult<T>>("api", {
    method,
    path,
    query: { language: locale, ...opts.query },
    body: opts.body ?? {},
    ...(opts.idempotencyKey ? { idempotency_key: opts.idempotencyKey } : {}),
  }, opts);
}

export function apiError(r: ApiResult<unknown>, fallback = tr("请求失败")): string {
  if (r.error === "api_unavailable") return tr("暂时无法连接服务，请检查网络后重试");
  if (r.error === "path not allowed") return tr("这个操作在路由器上不可用");
  return r.msg || r.error || fallback;
}

/** Hyper service timestamps are milliseconds in newer builds and seconds in older ones */
export function toDate(ts: number | null | undefined): Date | null {
  if (!ts || ts <= 0) return null;
  return new Date(ts > 1e12 ? ts : ts * 1000);
}

export function formatDate(ts: number | null | undefined): string {
  const d = toDate(ts);
  if (!d) return "--";
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function daysLeft(ts: number | null | undefined): number | null {
  const d = toDate(ts);
  return d ? Math.max(0, Math.ceil((d.getTime() - Date.now()) / 86400000)) : null;
}

/** Account and order/pre amounts use scale 100; plan lists declare their scale. */
export function money(amount: number | null | undefined, currency: CurrencyDisplay = DEFAULT_SITE_CURRENCY, scale = MINOR_AMOUNT_SCALE): string {
  return formatMinorMoney(amount, currency, scale, locale);
}

export const UNIT_LABEL: Record<string, string> = {
  Year: tr("年"),
  Month: tr("个月"),
  Week: tr("周"),
  Day: tr("天"),
  Hour: tr("小时"),
  NoLimit: "",
};

/** Order status, as the Hyper app reads it: 1 pending, 2/5 paid, 3 cancelled, 4 closed */
export function orderPhase(status: number | null | undefined): "pending" | "paid" | "closed" | "unknown" {
  if (status === 1) return "pending";
  if (status === 2 || status === 5) return "paid";
  if (status === 3 || status === 4) return "closed";
  return "unknown";
}
