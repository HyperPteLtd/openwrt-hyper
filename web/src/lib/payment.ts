/**
 * payment-v2, the way the Hyper app pays (hyper-tunnel packages/app-client/src/payment-v2):
 *   POST /v2/orders/purchase                      -> payment session
 *   GET  /v2/orders/{no}/payment                  -> poll the session
 *   POST /v2/orders/{no}/payment/selection        -> pick a crypto network / token
 *   POST /v2/orders/{no}/cancel
 * Writes carry an Idempotency-Key, so a repeated click or a retry never creates a second order.
 */
import { api, type ApiResult } from "./ppanel";

export type OrderStatus = "pending" | "paid" | "cancelled" | "expired" | "failed" | "fulfilled";
export type ActionState = "preparing" | "ready" | "paid" | "cancelled" | "expired" | "failed";

export interface SupportedAsset {
  network: string;
  display_name: string;
  tokens: string[];
}

export interface PaymentAction {
  type: "preparing" | "crypto_selection" | "crypto_transfer" | "redirect" | "qr" | "stripe" | "apple_iap" | "none";
  state: ActionState;
  provider?: string;
  payment_amount?: string;
  payment_currency?: string;
  expires_at?: number;
  retry_after_ms?: number;
  supported_assets?: SupportedAsset[];
  actual_amount?: string;
  token?: string;
  network?: string;
  receive_address?: string;
  deeplink?: string;
  provider_url?: string;
  qr_content?: string;
}

export interface PaymentSession {
  order_no: string;
  order_status: OrderStatus;
  amount?: string;
  currency?: string;
  quantity?: number;
  reused?: boolean;
  payment_action: PaymentAction;
}

const ERRORS: Record<string, string> = {
  "62025": "支付汇率暂不可用，请稍后重试",
  "62026": "当前支付渠道不可用",
  "62036": "当前支付渠道不可用",
  "62027": "支付订单已失效",
  "62029": "当前支付订单仍可继续，请勿重复下单",
  "62030": "支付订单正在恢复，请稍后刷新",
  "62032": "支付请求无法安全恢复，请重新操作",
  "62033": "支付请求无法安全恢复，请重新操作",
  "62034": "当前币种或网络不可用",
  "62037": "当前币种或网络不可用",
  "62035": "订单已支付，正在同步权益",
};

export const ORDER_ALREADY_PAID = 62035;
export const SESSION_RECOVERING = 62030;

export function paymentError(r: ApiResult<unknown>, fallback: string): string {
  if (r.error === "api_base_not_set") return "还没有设置服务地址。";
  if (r.error === "path not allowed") return "这个操作在路由器上不可用。";
  const known = r.code != null ? ERRORS[String(r.code)] : undefined;
  if (known) return known;
  /* server text only when it is short and carries nothing sensitive */
  const msg = (r.msg || "").trim();
  if (msg && msg.length <= 240 && !/(https?:\/\/|authorization|bearer\s|client_secret|publishable_key)/i.test(msg)) return msg;
  return fallback;
}

/** Idempotency key in the app's format; crypto.randomUUID needs HTTPS, LuCI is often plain HTTP */
export function newIdempotencyKey(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `checkout:v2:${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export function isSession(v: unknown): v is PaymentSession {
  const s = v as PaymentSession | null;
  return !!s && typeof s.order_no === "string" && !!s.order_no && typeof s.payment_action === "object" && !!s.payment_action;
}

export function isPaid(s: PaymentSession | null): boolean {
  return !!s && (s.payment_action.state === "paid" || s.order_status === "paid" || s.order_status === "fulfilled");
}

export function isTerminal(s: PaymentSession | null): boolean {
  if (!s) return false;
  return ["paid", "fulfilled", "cancelled", "expired", "failed"].includes(s.order_status) || ["paid", "cancelled", "expired", "failed"].includes(s.payment_action.state);
}

export function isPreparing(s: PaymentSession): boolean {
  return !isTerminal(s) && (s.payment_action.type === "preparing" || s.payment_action.state === "preparing");
}

const PREPARING_WINDOW_MS = 15_000;

/** Next poll in ms, or null to stop (terminal, or preparing for too long: the user retries by hand) */
export function nextPollDelay(s: PaymentSession | null, preparingSince: number | null, now: number): number | null {
  if (!s || isTerminal(s)) return null;
  if (isPreparing(s)) {
    if (preparingSince != null && now - preparingSince >= PREPARING_WINDOW_MS) return null;
    const wanted = s.payment_action.type === "preparing" ? s.payment_action.retry_after_ms ?? 1000 : 1000;
    return Math.max(250, Math.min(5000, wanted));
  }
  return 5000;
}

export function remainingSeconds(s: PaymentSession | null, nowMs: number): number | null {
  const at = s?.payment_action.expires_at;
  return at ? Math.max(0, Math.ceil((at * 1000 - nowMs) / 1000)) : null;
}

export function formatRemaining(sec: number): string {
  return `${String(Math.floor(sec / 60)).padStart(2, "0")}:${String(sec % 60).padStart(2, "0")}`;
}

const SYMBOL: Record<string, string> = { CNY: "¥", USD: "$", EUR: "€", HKD: "HK$", JPY: "¥", GBP: "£" };

export function decimalMoney(amount?: string, currency?: string): string | null {
  if (!amount || !currency) return null;
  const sym = SYMBOL[currency.toUpperCase()];
  return sym ? `${sym}${amount}` : `${amount} ${currency.toUpperCase()}`;
}

/** What the payer actually sends on the chosen channel */
export function channelAmount(s: PaymentSession): string | null {
  const a = s.payment_action;
  if (a.type === "crypto_transfer" && a.actual_amount && a.token) return `${a.actual_amount} ${a.token}`;
  return decimalMoney(a.payment_amount, a.payment_currency);
}

/** Only https:// opens from a browser; wallet schemes can still be shown as a QR code */
export function httpsTarget(value?: string): string | null {
  const raw = (value || "").trim();
  if (!raw || /[\u0000- \u007f\\]/.test(raw)) return null;
  try {
    const u = new URL(raw);
    if (u.protocol !== "https:" || u.username || u.password) return null;
    return u.toString();
  } catch {
    return null;
  }
}

export function walletTarget(value?: string): string | null {
  const raw = (value || "").trim();
  return /^(alipay|alipays|weixin|wxp):\/\/[^\s]+$/i.test(raw) ? raw : null;
}

/* ---------- API ---------- */

const orderPath = (no: string, tail: string) => `/v2/orders/${encodeURIComponent(no)}/${tail}`;

export function purchase(body: { subscribe_id: number; payment_id: number; quantity: number; coupons?: string[] }, key: string) {
  return api<PaymentSession>("POST", "/v2/orders/purchase", {
    body: { subscribe_id: body.subscribe_id, payment_id: body.payment_id, quantity: body.quantity, ...(body.coupons?.length ? { coupons: body.coupons } : {}) },
    idempotencyKey: key,
  });
}

export function getPayment(no: string) {
  return api<PaymentSession>("GET", orderPath(no, "payment"));
}

export function selectAsset(no: string, token: string, network: string, key: string) {
  return api<PaymentSession>("POST", orderPath(no, "payment/selection"), { body: { token: token.toUpperCase(), network: network.toLowerCase() }, idempotencyKey: key });
}

export function cancelOrder(no: string, key: string) {
  return api<PaymentSession>("POST", orderPath(no, "cancel"), { idempotencyKey: key });
}

/* ---------- the open order survives a page reload (this browser only) ---------- */

const PENDING_KEY = "hyper.payment.pending";

export interface PendingOrder {
  order_no: string;
  plan: string;
  quantity: number;
  unit: string;
  time: number;
}

export function savePending(p: PendingOrder | null): void {
  try {
    if (p) localStorage.setItem(PENDING_KEY, JSON.stringify(p));
    else localStorage.removeItem(PENDING_KEY);
  } catch {
    /* storage blocked: the order just will not be resumed after a reload */
  }
}

export function loadPending(): PendingOrder | null {
  try {
    const p = JSON.parse(localStorage.getItem(PENDING_KEY) || "null") as PendingOrder | null;
    if (p && typeof p.order_no === "string" && Date.now() - p.time < 86400e3) return p;
  } catch {
    /* ignore */
  }
  return null;
}

/** Copy, with a fallback for plain-HTTP LuCI where navigator.clipboard is missing */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}
