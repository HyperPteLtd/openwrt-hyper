import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { useStore } from "../lib/store";
import { QrCode } from "../components/QrCode";
import { Notice } from "../components/ui";
import { app } from "../lib/app";
import {
  cancelOrder, channelAmount, copyText, decimalMoney, formatRemaining, getPayment, httpsTarget, isPaid, isPreparing, isSession, isTerminal,
  loadPending, newIdempotencyKey, nextPollDelay, ORDER_ALREADY_PAID, paymentError, purchase, remainingSeconds, savePending, selectAsset,
  SESSION_RECOVERING, walletTarget, type PaymentSession,
} from "../lib/payment";
import { api, apiError, money, UNIT_LABEL } from "../lib/ppanel";
import { call } from "../lib/rpc";

interface Tier {
  quantity: number;
  sell_price: number;
  original_price?: number;
  recommend?: boolean;
}
interface Plan {
  id: number;
  name: string;
  description?: string;
  unit_time?: string;
  unit_price?: number;
  device_limit?: number;
  traffic?: number;
  discount?: Tier[];
}
interface Units {
  amount_scale?: number;
  currency_symbol?: string;
}
interface PayMethod {
  id: number;
  name: string;
  platform?: string;
  description?: string;
}
interface Preview {
  price?: number;
  amount?: number;
  discount?: number;
  coupon_discount?: number;
  fee_amount?: number;
}

type Step = "pick" | "confirm" | "order" | "done" | "closed";

/* Apple in-app purchase only works inside the iOS app */
function isAppleIap(m: PayMethod): boolean {
  const meta = `${m.platform ?? ""} ${m.name ?? ""} ${m.description ?? ""}`.toLowerCase().replace(/[_\-.]+/g, " ").replace(/\s+/g, " ");
  return /apple\s+(?:iap|in\s+app|purchase)|app\s+store|苹果(?:内购|应用内购买)/.test(meta);
}

const Row = ({ label, value, strong }: { label: string; value: string; strong?: boolean }) => (
  <div style={{ display: "flex", justifyContent: "space-between", gap: "16px" }}>
    <dt class="muted">{label}</dt>
    <dd class={strong ? undefined : "mono"} style={{ margin: 0, textAlign: "right", overflowWrap: "anywhere", fontWeight: strong ? 700 : 400, color: strong ? "var(--accent)" : undefined }}>{value}</dd>
  </div>
);

/*
 * Purchase flow, same as the Hyper app (payment-v2):
 *   order/pre (price preview) -> v2/orders/purchase -> show the payment action
 *   (QR code, payment page, crypto transfer) -> poll v2/orders/{no}/payment until paid
 */
export function Plans() {
  const plans = useSignal<Plan[]>([]);
  const units = useSignal<Units>({});
  const methods = useSignal<PayMethod[]>([]);
  const sel = useStore({ plan: 0, quantity: 0, payment: 0, coupon: "" });
  const step = useSignal<Step>("pick");
  const preview = useSignal<Preview | null>(null);
  const busy = useSignal(false);
  const msg = useStore({ text: "", tone: "error" as "error" | "ok" | "warn" });
  const purchaseKey = useSignal("");
  const pay = useStore({
    session: null as PaymentSession | null,
    preparingSince: null as number | null,
    stalled: false,
    rev: 0,
    plan: "",
    duration: "",
  });
  const asset = useStore({ network: "", token: "", label: "" });
  const now = useSignal(Date.now());
  const askCancel = useSignal(false);
  const copied = useSignal<"" | "ok" | "fail">("");

  const setSession = (s: PaymentSession) => {
    pay.session = s;
    pay.preparingSince = isPreparing(s) ? (pay.preparingSince ?? Date.now()) : null;
    pay.stalled = false;
    pay.rev++;
    const assets = s.payment_action.supported_assets ?? [];
    if (s.payment_action.type === "crypto_selection" && !assets.some((a) => a.network === asset.network && a.tokens.includes(asset.token))) {
      asset.network = assets[0]?.network ?? "";
      asset.token = assets[0]?.tokens[0] ?? "";
    }
    if (isTerminal(s)) {
      savePending(null);
      step.value = isPaid(s) ? "done" : "closed";
    }
  };

  useEffect(() => {
    load();
  }, []);

  async function load() {
    try {
      const [p, m] = await Promise.all([
        api<{ list: Plan[]; units?: Units }>("GET", "/v1/public/subscribe/list", { query: { language: "zh-CN" } }),
        api<{ list: PayMethod[] }>("GET", "/v1/public/payment/methods"),
      ]);
      if (!p.ok) msg.text = apiError(p, "套餐加载失败");
      plans.value = (p.data?.list ?? []).filter((x) => (x.discount?.length ?? 0) > 0);
      units.value = p.data?.units ?? {};
      methods.value = (m.data?.list ?? []).filter((x) => !isAppleIap(x));
      const first = plans.value[0];
      if (first) {
        const tier = first.discount!.find((t) => t.recommend) ?? first.discount![0];
        sel.plan = first.id;
        sel.quantity = tier.quantity;
      }
      if (methods.value[0]) sel.payment = methods.value[0].id;

      /* an order opened earlier in this browser and not finished yet */
      const pending = loadPending();
      if (pending) {
        const r = await getPayment(pending.order_no);
        if (r.ok && isSession(r.data) && !isTerminal(r.data)) {
          pay.plan = pending.plan;
          pay.duration = `${pending.quantity} ${pending.unit}`;
          await setSession(r.data);
          step.value = "order";
        } else savePending(null);
      }
    } catch (e) {
      msg.text = (e as Error).message;
    }
  }

  const scale = units.value.amount_scale || 100;
  const sym = units.value.currency_symbol || "¥";
  const plan = plans.value.find((p) => p.id === sel.plan);
  const tier = plan?.discount?.find((t) => t.quantity === sel.quantity);
  const unit = UNIT_LABEL[plan?.unit_time || ""] ?? plan?.unit_time ?? "";
  const coupons = () => (sel.coupon.trim() ? [sel.coupon.trim()] : []);

  const toConfirm = async () => {
    busy.value = true;
    msg.text = "";
    msg.tone = "error";
    const r = await api<Preview>("POST", "/v1/public/order/pre", {
      body: { subscribe_id: sel.plan, quantity: sel.quantity, payment: sel.payment, coupons: coupons() },
    });
    busy.value = false;
    if (!r.ok) {
      msg.tone = "error";
      msg.text = apiError(r, "价格计算失败");
      return;
    }
    preview.value = r.data ?? null;
    /* one key per confirmed selection: retrying the same order never creates a second one */
    purchaseKey.value = newIdempotencyKey();
    step.value = "confirm";
  };

  const payNow = async () => {
    busy.value = true;
    msg.text = "";
    msg.tone = "error";
    try {
      const r = await purchase({ subscribe_id: sel.plan, payment_id: sel.payment, quantity: sel.quantity, coupons: coupons() }, purchaseKey.value);
      if (!r.ok) {
        if (r.code === ORDER_ALREADY_PAID) step.value = "done";
        else msg.text = paymentError(r, "下单失败");
        return;
      }
      if (!isSession(r.data)) {
        msg.text = "支付服务返回的数据不完整，请稍后重试。";
        return;
      }
      pay.plan = plan?.name ?? "";
      pay.duration = `${sel.quantity} ${unit}`;
      pay.preparingSince = null;
      savePending({ order_no: r.data.order_no, plan: pay.plan, quantity: sel.quantity, unit, time: Date.now() });
      await setSession(r.data);
      if (!isTerminal(r.data)) step.value = "order";
    } catch (e) {
      msg.text = (e as Error).message;
    } finally {
      busy.value = false;
    }
  };

  /* Poll the payment session while the order is open */
  const rev = pay.rev;
  const stepNow = step.value;
  useEffect(() => {
    if (stepNow !== "order" || !pay.session) return;
    const no = pay.session.order_no;
    const delay = nextPollDelay(pay.session, pay.preparingSince, Date.now());
    if (delay == null) {
      if (isPreparing(pay.session)) pay.stalled = true;
      return;
    }
    const t = setTimeout(async () => {
      try {
        const r = await getPayment(no);
        if (step.value !== "order" || pay.session?.order_no !== no) return;
        if (r.ok && isSession(r.data) && r.data.order_no === no) {
          await setSession(r.data);
          return;
        }
        if (r.code === ORDER_ALREADY_PAID) {
          savePending(null);
          step.value = "done";
          return;
        }
        if (r.code != null && r.code !== SESSION_RECOVERING && String(r.code).startsWith("62")) {
          msg.tone = "error";
          msg.text = paymentError(r, "支付状态刷新失败");
          return;
        }
      } catch {
        /* rpcd hiccup: try again on the next round */
      }
      pay.rev++;
    }, delay);
    return () => clearTimeout(t);
  }, [rev, stepNow]);

  /* Countdown */
  useEffect(() => {
    if (stepNow !== "order") return;
    const id = setInterval(() => (now.value = Date.now()), 1000);
    return () => clearInterval(id);
  }, [stepNow]);

  /* Paid: pull the new subscription onto the router */
  useEffect(() => {
    if (stepNow !== "done") return;
    savePending(null);
    call("sync_subscription")
      .catch(() => {
        /* the overview shows the sync state */
      })
      .finally(() => app.tick++);
  }, [stepNow]);

  const retryPreparing = async () => {
    if (!pay.session) return;
    pay.preparingSince = Date.now();
    pay.stalled = false;
    const r = await getPayment(pay.session.order_no);
    if (r.ok && isSession(r.data)) await setSession(r.data);
    else {
      msg.tone = "error";
      msg.text = paymentError(r, "支付状态刷新失败");
    }
  };

  const chooseAsset = async () => {
    if (!pay.session || !asset.network || !asset.token) return;
    asset.label = pay.session.payment_action.supported_assets?.find((n) => n.network === asset.network)?.display_name ?? "";
    busy.value = true;
    msg.text = "";
    msg.tone = "error";
    const r = await selectAsset(pay.session.order_no, asset.token, asset.network, newIdempotencyKey());
    busy.value = false;
    if (r.ok && isSession(r.data)) await setSession(r.data);
    else msg.text = paymentError(r, "当前币种或网络不可用");
  };

  const cancel = async () => {
    if (!pay.session) return;
    busy.value = true;
    const r = await cancelOrder(pay.session.order_no, newIdempotencyKey());
    busy.value = false;
    askCancel.value = false;
    if (!r.ok && r.code !== ORDER_ALREADY_PAID) {
      msg.tone = "error";
      msg.text = paymentError(r, "取消失败，请稍后再试");
      return;
    }
    if (r.code === ORDER_ALREADY_PAID || (isSession(r.data) && isPaid(r.data))) {
      step.value = "done";
      return;
    }
    savePending(null);
    pay.session = null;
    msg.text = "订单已取消。";
    msg.tone = "ok";
    step.value = "pick";
  };

  const copy = async (text: string) => {
    copied.value = (await copyText(text)) ? "ok" : "fail";
    setTimeout(() => (copied.value = ""), 3000);
  };

  const s = pay.session;
  const a = s?.payment_action;
  const left = remainingSeconds(s, now.value);
  const idx = { pick: 0, confirm: 1, order: 2, done: 3, closed: 2 }[step.value];

  return (
    <main class="page">
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "12px" }}>
        <a href="#/account" style={{ minHeight: "44px", display: "inline-flex", alignItems: "center" }}>‹ 账户与订阅</a>
        <span style={{ flexGrow: 1 }} />
        <ol aria-label="购买步骤" style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexWrap: "wrap", gap: "6px", fontSize: "13px" }}>
          {["选择套餐", "确认订单", "支付", "完成"].map((label, i) => (
            <li key={label} aria-current={i === idx ? "step" : undefined} style={{ padding: "6px 12px", borderRadius: "14px", background: i === idx ? "var(--accent)" : i < idx ? "var(--control)" : "var(--surface)", color: i === idx ? "var(--accent-ink)" : i < idx ? "var(--text)" : "var(--muted)", fontWeight: i === idx ? 600 : 400 }}>
              {i + 1} {label}
            </li>
          ))}
        </ol>
      </div>
      <Notice text={msg.text} tone={msg.tone} />

      {step.value === "pick" && (
        <section class="card" style={{ gap: "24px" }}>
          <h2 style={{ fontSize: "24px" }}>选择会员套餐</h2>
          {plans.value.map((p) => (
            <div key={p.id} style={{ display: "flex", flexDirection: "column", gap: "12px" }}>
              <div style={{ display: "flex", alignItems: "baseline", gap: "12px", flexWrap: "wrap" }}>
                <b style={{ fontSize: "17px" }}>{p.name}</b>
                <span class="muted small">{p.device_limit ? `${p.device_limit} 台设备` : ""}</span>
              </div>
              <div role="radiogroup" aria-label={p.name} style={{ display: "flex", flexWrap: "wrap", gap: "12px" }}>
                {(p.discount ?? []).map((t) => {
                  const on = sel.plan === p.id && sel.quantity === t.quantity;
                  return (
                    <button
                      key={t.quantity}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      class="option"
                      style={{ flex: "1 1 180px", flexDirection: "column", alignItems: "center", gap: "8px", padding: "20px 12px", minHeight: "150px", textAlign: "center" }}
                      onClick={() => {
                        sel.plan = p.id;
                        sel.quantity = t.quantity;
                      }}
                    >
                      <span>{t.quantity} {UNIT_LABEL[p.unit_time || ""] ?? p.unit_time}</span>
                      <b style={{ fontSize: "28px", color: on ? "var(--accent)" : "var(--text)", fontVariantNumeric: "tabular-nums" }}>{money(t.sell_price, sym, scale)}</b>
                      {t.original_price && t.original_price > t.sell_price ? <s class="muted small">{money(t.original_price, sym, scale)}</s> : null}
                      {t.recommend && <span class="small" style={{ padding: "3px 10px", borderRadius: "10px", background: "#2e3a0e", color: "var(--accent)" }}>推荐</span>}
                    </button>
                  );
                })}
              </div>
            </div>
          ))}
          {!plans.value.length && !msg.text && <span class="muted">加载中…</span>}
          {plan && tier && (
            <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", justifyContent: "flex-end", gap: "16px 32px" }}>
              <span class="muted">{plan.name} · {tier.quantity} {unit}</span>
              <b style={{ fontSize: "32px", color: "var(--accent)" }}>{money(tier.sell_price, sym, scale)}</b>
              <button type="button" class="btn primary big" style={{ minWidth: "200px" }} disabled={busy.value || !methods.value.length} onClick={() => toConfirm()}>
                {busy.value ? "请稍候…" : "购买"}
              </button>
            </div>
          )}
          {!methods.value.length && plans.value.length > 0 && <span class="muted small">没有可用的支付方式。</span>}
        </section>
      )}

      {step.value === "confirm" && plan && (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "2 1 460px" }}>
            <h2 style={{ fontSize: "22px" }}>确认订单</h2>
            <dl style={{ margin: 0, display: "flex", flexDirection: "column", gap: "12px" }}>
              <div style={{ display: "flex", justifyContent: "space-between" }}><dt class="muted">套餐</dt><dd style={{ margin: 0 }}>{plan.name}</dd></div>
              <div style={{ display: "flex", justifyContent: "space-between" }}><dt class="muted">时长</dt><dd style={{ margin: 0 }}>{sel.quantity} {unit}</dd></div>
              <div style={{ display: "flex", justifyContent: "space-between" }}><dt class="muted">原价</dt><dd style={{ margin: 0 }}>{money(preview.value?.price, sym, scale)}</dd></div>
              {!!preview.value?.discount && <div style={{ display: "flex", justifyContent: "space-between" }}><dt class="muted">优惠</dt><dd style={{ margin: 0 }}>-{money(preview.value.discount, sym, scale)}</dd></div>}
              {!!preview.value?.coupon_discount && <div style={{ display: "flex", justifyContent: "space-between" }}><dt class="muted">优惠码</dt><dd style={{ margin: 0 }}>-{money(preview.value.coupon_discount, sym, scale)}</dd></div>}
              {!!preview.value?.fee_amount && <div style={{ display: "flex", justifyContent: "space-between" }}><dt class="muted">手续费</dt><dd style={{ margin: 0 }}>{money(preview.value.fee_amount, sym, scale)}</dd></div>}
            </dl>
            <label class="field">
              优惠码
              <span style={{ display: "flex", gap: "10px" }}>
                <input class="input" value={sel.coupon} onInput={(e) => (sel.coupon = e.currentTarget.value)} placeholder="输入优惠码" />
                <button type="button" class="btn" style={{ borderRadius: "12px", minHeight: "48px", flexShrink: 0 }} disabled={busy.value} onClick={() => toConfirm()}>使用</button>
              </span>
            </label>
            <fieldset style={{ border: 0, margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: "8px" }}>
              <legend class="muted small" style={{ padding: "0 0 8px" }}>支付方式</legend>
              {methods.value.map((m) => (
                <button key={m.id} type="button" role="radio" aria-checked={sel.payment === m.id} class="option" onClick={async () => { sel.payment = m.id; await toConfirm(); }}>
                  <span class="grow"><span>{m.name}</span>{m.description && <span class="muted small">{m.description}</span>}</span>
                </button>
              ))}
            </fieldset>
          </section>
          <section class="card" style={{ flex: "1 1 300px" }}>
            <span class="muted">应付</span>
            <b style={{ fontSize: "36px", color: "var(--accent)" }}>{money(preview.value?.amount, sym, scale)}</b>
            <p class="muted small" style={{ margin: 0 }}>点“去支付”后生成订单：扫码支付会在这里显示二维码，其他方式会给出支付页面或收款地址。这个页面会自动检查支付结果。</p>
            <button type="button" class="btn primary big" disabled={busy.value} onClick={() => payNow()}>{busy.value ? "请稍候…" : "去支付"}</button>
            <button type="button" class="btn" onClick={() => (step.value = "pick")}>返回修改</button>
          </section>
        </div>
      )}

      {step.value === "order" && s && a && (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "2 1 460px", alignItems: "center", textAlign: "center", gap: "18px" }}>
            <div style={{ display: "flex", flexDirection: "column", gap: "6px", alignItems: "center" }}>
              <b style={{ fontSize: "24px" }}>等待支付</b>
              <span class="muted small">完成支付后这里会自动更新，路由器随后同步新的订阅。</span>
              {left != null && (
                <span aria-label="剩余支付时间" style={{ fontSize: "28px", fontWeight: 800, color: left > 60 ? "var(--accent)" : "var(--danger-text)", fontVariantNumeric: "tabular-nums" }}>
                  {formatRemaining(left)}
                </span>
              )}
            </div>

            {a.type === "preparing" &&
              (pay.stalled ? (
                <div style={{ display: "flex", flexDirection: "column", gap: "10px", alignItems: "center" }}>
                  <span class="muted">支付参数还没准备好。</span>
                  <button type="button" class="btn primary" onClick={() => retryPreparing()}>重试</button>
                </div>
              ) : (
                <span class="muted" role="status">正在生成支付参数…</span>
              ))}

            {a.type === "qr" && a.qr_content && (
              <>
                <QrCode value={a.qr_content} label="支付二维码" />
                <span class="muted">请用手机扫码完成支付</span>
              </>
            )}

            {a.type === "redirect" &&
              (() => {
                const https = httpsTarget(a.provider_url) ?? httpsTarget(a.deeplink);
                const wallet = walletTarget(a.deeplink);
                if (!https && !wallet) return <span class="muted">支付链接无效，请取消订单后换一种支付方式。</span>;
                return (
                  <>
                    {https && (
                      <a class="btn primary big" href={https} target="_blank" rel="noopener noreferrer" style={{ minWidth: "240px" }}>
                        打开支付页面
                      </a>
                    )}
                    <QrCode value={https ?? wallet!} label="支付链接二维码" size={180} />
                    <span class="muted small">{https ? "也可以用手机扫码，在手机上完成支付" : "请用手机扫码，在手机上完成支付"}</span>
                  </>
                );
              })()}

            {a.type === "stripe" &&
              (httpsTarget(a.provider_url) ? (
                <a class="btn primary big" href={httpsTarget(a.provider_url)!} target="_blank" rel="noopener noreferrer" style={{ minWidth: "240px" }}>
                  打开支付页面
                </a>
              ) : (
                <span class="muted">这种支付方式请在 Hyper App 里完成，或取消订单后换一种支付方式。</span>
              ))}

            {a.type === "crypto_selection" && (
              <div style={{ display: "flex", flexDirection: "column", gap: "14px", width: "100%", maxWidth: "460px", textAlign: "left" }}>
                <fieldset style={{ border: 0, margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: "8px" }}>
                  <legend class="muted small" style={{ padding: "0 0 8px" }}>网络</legend>
                  {(a.supported_assets ?? []).map((n) => (
                    <button
                      key={n.network}
                      type="button"
                      role="radio"
                      aria-checked={asset.network === n.network}
                      class="option"
                      onClick={() => {
                        asset.network = n.network;
                        if (!n.tokens.includes(asset.token)) asset.token = n.tokens[0] ?? "";
                      }}
                    >
                      <span class="grow"><span>{n.display_name}</span></span>
                    </button>
                  ))}
                </fieldset>
                <fieldset style={{ border: 0, margin: 0, padding: 0, display: "flex", flexWrap: "wrap", gap: "8px" }}>
                  <legend class="muted small" style={{ padding: "0 0 8px" }}>币种</legend>
                  {((a.supported_assets ?? []).find((n) => n.network === asset.network)?.tokens ?? []).map((t) => (
                    <button key={t} type="button" role="radio" aria-checked={asset.token === t} class="option" style={{ flex: "0 0 auto", minHeight: "44px", padding: "0 18px" }} onClick={() => (asset.token = t)}>
                      {t}
                    </button>
                  ))}
                </fieldset>
                <button type="button" class="btn primary big" disabled={busy.value || !asset.token} onClick={() => chooseAsset()}>
                  {busy.value ? "请稍候…" : "获取收款地址"}
                </button>
              </div>
            )}

            {a.type === "crypto_transfer" && a.receive_address && (
              <>
                <b style={{ fontSize: "26px", fontVariantNumeric: "tabular-nums" }}>{a.actual_amount} {a.token}</b>
                <QrCode value={a.receive_address} label="收款地址二维码" />
                <span style={{ display: "flex", flexWrap: "wrap", gap: "8px", alignItems: "center", justifyContent: "center", maxWidth: "100%" }}>
                  <code class="mono small" style={{ overflowWrap: "anywhere", padding: "8px 12px", borderRadius: "10px", background: "var(--card-2)", userSelect: "all" }}>{a.receive_address}</code>
                  <button type="button" class="btn" onClick={() => copy(a.receive_address!)}>{copied.value === "ok" ? "已复制" : "复制地址"}</button>
                </span>
                <span class="small" role="status" style={{ minHeight: "1.4em", color: "var(--muted)" }}>
                  {copied.value === "fail" ? "浏览器不允许自动复制，请点一下地址全选后手动复制。" : ""}
                </span>
                <span class="small" style={{ color: "#ffb547", maxWidth: "460px" }}>
                  只能用 {(asset.network === a.network && asset.label) || (a.network || "").toUpperCase()} 网络转入 {a.token}，金额要和上面完全一致，否则无法自动到账。
                </span>
              </>
            )}

            {a.type === "apple_iap" && <span class="muted">Apple 内购只能在 iPhone / iPad 的 Hyper App 里完成。可以取消这个订单，换一种支付方式。</span>}
            {a.type === "none" && <span class="muted" role="status">正在同步支付状态…</span>}
          </section>

          <section class="card" style={{ flex: "1 1 300px" }}>
            <h2>订单</h2>
            <dl style={{ margin: 0, display: "flex", flexDirection: "column", gap: "12px" }}>
              <Row label="订单号" value={s.order_no} />
              {pay.plan && <Row label="套餐" value={pay.plan} />}
              {pay.duration && <Row label="时长" value={pay.duration} />}
              {decimalMoney(s.amount, s.currency) && <Row label="订单金额" value={decimalMoney(s.amount, s.currency)!} />}
              {channelAmount(s) && <Row label="实付" value={channelAmount(s)!} strong />}
            </dl>
            {askCancel.value ? (
              <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
                <span class="small">确定取消这个订单？已经付款的话请不要取消。</span>
                <span style={{ display: "flex", gap: "8px" }}>
                  <button type="button" class="btn" style={{ flex: 1 }} onClick={() => (askCancel.value = false)}>继续支付</button>
                  <button type="button" class="btn" style={{ flex: 1, color: "var(--danger-text)" }} disabled={busy.value} onClick={() => cancel()}>取消订单</button>
                </span>
              </div>
            ) : (
              <button type="button" class="btn ghost" style={{ color: "var(--danger-text)" }} disabled={busy.value} onClick={() => (askCancel.value = true)}>取消订单</button>
            )}
          </section>
        </div>
      )}

      {step.value === "done" && (
        <section class="card empty" style={{ minHeight: "420px" }}>
          <b style={{ fontSize: "26px", color: "var(--accent)" }}>订阅成功</b>
          <p class="muted" style={{ margin: 0 }}>{pay.plan || plan?.name} 已生效，路由器正在同步新的节点。</p>
          <div style={{ display: "flex", gap: "10px" }}>
            <a class="btn primary" href="#/overview">返回概览</a>
            <a class="btn" href="#/account">查看账户</a>
          </div>
        </section>
      )}

      {step.value === "closed" && (
        <section class="card empty" style={{ minHeight: "320px" }}>
          <b style={{ fontSize: "22px" }}>{s?.order_status === "expired" || a?.state === "expired" ? "订单已超时" : s?.order_status === "failed" || a?.state === "failed" ? "支付失败" : "订单已关闭"}</b>
          <p class="muted" style={{ margin: 0 }}>
            {s?.order_status === "failed" || a?.state === "failed" ? "如果已经扣款，请联系客服并提供订单号。" : "没有完成支付，订单已关闭。"}
          </p>
          {s && <span class="mono small muted">订单号 {s.order_no}</span>}
          <button type="button" class="btn primary" onClick={() => (step.value = "pick")}>重新选择</button>
        </section>
      )}
    </main>
  );
}
