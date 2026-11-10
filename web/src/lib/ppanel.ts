/** PPanel calls through luci.hyper `api` (allowlisted paths, token added on the router). */
import { call } from "./rpc";

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
  opts: { query?: Record<string, unknown>; body?: unknown; idempotencyKey?: string } = {},
): Promise<ApiResult<T>> {
  return call<ApiResult<T>>("api", {
    method,
    path,
    query: opts.query ?? {},
    body: opts.body ?? {},
    ...(opts.idempotencyKey ? { idempotency_key: opts.idempotencyKey } : {}),
  });
}

export function apiError(r: ApiResult<unknown>, fallback = "请求失败"): string {
  if (r.error === "api_base_not_set") return "还没有设置服务地址。";
  if (r.error === "path not allowed") return "这个操作在路由器上不可用。";
  return r.msg || r.error || fallback;
}

/** PPanel timestamps are milliseconds in newer builds and seconds in older ones */
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

/** Amounts are minor units (scale 100 unless the plan list says otherwise) */
export function money(amount: number | null | undefined, symbol = "¥", scale = 100): string {
  if (amount == null) return "--";
  return `${symbol}${(amount / scale).toFixed(2)}`;
}

export const UNIT_LABEL: Record<string, string> = {
  Year: "年",
  Month: "个月",
  Week: "周",
  Day: "天",
  Hour: "小时",
  NoLimit: "",
};

/** Order status, as the Hyper app reads it: 1 pending, 2/5 paid, 3 cancelled, 4 closed */
export function orderPhase(status: number | null | undefined): "pending" | "paid" | "closed" | "unknown" {
  if (status === 1) return "pending";
  if (status === 2 || status === 5) return "paid";
  if (status === 3 || status === 4) return "closed";
  return "unknown";
}
