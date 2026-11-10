/**
 * In-memory stand-in for luci.hyper, used when the app is opened outside LuCI
 * (vite dev / preview). Sample data follows the design canvas; shapes follow
 * the rpcd methods and the PPanel API.
 */
import type { NodeInfo, Status } from "./types";

const now = () => Math.floor(Date.now() / 1000);
const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));

const nodes: NodeInfo[] = [
  { name: "pp_jp1", label: "日本 · 东京 01", type: "anytls", country: "JPN" },
  { name: "pp_jp2", label: "日本 · 大阪", type: "anytls", country: "JPN" },
  { name: "pp_hk1", label: "中国香港 01", type: "hysteria2", country: "HKG" },
  { name: "pp_sg1", label: "新加坡 01", type: "anytls", country: "SGP" },
  { name: "pp_sg2", label: "新加坡 02", type: "vless", reality: true, country: "SGP" },
  { name: "pp_kr1", label: "韩国 · 首尔", type: "anytls", country: "KOR" },
  { name: "pp_us1", label: "美国 · 洛杉矶", type: "trojan", transport: "ws", country: "USA" },
  { name: "pp_de1", label: "德国 · 法兰克福", type: "anytls", country: "DEU" },
  { name: "pp_gb1", label: "英国 · 伦敦", type: "anytls", country: "GBR" },
];
const latency: Record<string, number | null> = {
  pp_jp1: 46, pp_jp2: 58, pp_hk1: 36, pp_sg1: 52, pp_sg2: 61, pp_kr1: 62, pp_us1: 128, pp_de1: 156, pp_gb1: null,
};

const state: Status = {
  enabled: true,
  running: true,
  memory_kb: 38120,
  uptime: 2 * 86400 + 4 * 3600 + 12 * 60,
  main_node: "pp_jp1",
  main_node_label: "日本 · 东京 01",
  mode: "smart",
  signed_in: true,
  email: "astra345@gmail.com",
  api_base: "https://api.example.com",
  sync: {
    ok: true, time: now() - 120, count: nodes.length, added: 1, removed: 0,
    ...({ traffic_used: "42.6 GB", traffic_total: "不限", countries: ["JPN", "HKG", "SGP", "KOR", "USA", "DEU", "GBR"] } as object),
  },
  health: { state: "ok", failures: 0, time: now() },
  singbox_version: "1.15.0-alpha.13",
  version: "0.1.0-r1",
};

/* last check_update, like /var/run/hyper/update_check.json */
let lastCheck: { time: number; latest: string | null } | null = null;
const MOCK_LATEST = "0.2.0";

/** Like versionCompare() in hyper.uc for plain x.y.z: ignores "v" and "-rN" */
function newer(a: string, b: string): boolean {
  const parts = (v: string) => v.replace(/^v/, "").replace(/-r\d+$/, "").split(".").map(Number);
  const x = parts(a), y = parts(b);
  for (let i = 0; i < Math.max(x.length, y.length); i++) if ((x[i] ?? 0) !== (y[i] ?? 0)) return (x[i] ?? 0) > (y[i] ?? 0);
  return false;
}

const config: Record<string, string> = {
  "config.auto_reconnect": "1",
  "control.proxy_router_self": "1",
  "config.ipv6_support": "0",
  "config.log_level": "warn",
  "config.proxy_mode": "redirect_tproxy",
  "config.dns_server": "https://1.1.1.1/dns-query",
  "config.china_dns_server": "223.5.5.5",
  "subscription.auto_update": "1",
  "subscription.auto_update_time": "4",
  "subscription.update_via_proxy": "0",
  "update.auto_check": "1",
};

const rules = [
  { key: "ads", policy: "PROXY", server_enabled: true, enabled: true, count: 1820, source: "url" },
  { key: "cn-direct", policy: "DIRECT", server_enabled: true, enabled: true, count: 412, source: "url" },
  { key: "streaming", policy: "PROXY", server_enabled: true, enabled: true, count: 96, source: "url" },
  { key: "ai", policy: "PROXY", server_enabled: true, enabled: false, count: 14, source: "inline" },
];
const lists: Record<string, string> = { direct_list: "company.com\nlocal-bank", proxy_list: "openai.com" };

const clients = [
  { mac: "a4:5e:60:1c:22:9b", ip: "192.168.1.23", name: "客厅电视", online: true },
  { mac: "3c:22:fb:7a:10:4e", ip: "192.168.1.31", name: "Astra-iPhone", online: true },
  { mac: "f0:18:98:3d:6a:01", ip: "192.168.1.40", name: "MacBook-Pro", online: true },
  { mac: "88:66:5a:21:c4:7f", ip: "192.168.1.44", name: "iPad", online: false },
  { mac: "00:d9:d1:8b:33:e2", ip: "192.168.1.52", name: "PS5", online: true },
  { mac: "00:11:32:9f:0b:5d", ip: "192.168.1.10", name: null, online: true },
];
let access: { mode: string; direct: string[] } = { mode: "except_listed", direct: ["00:d9:d1:8b:33:e2"] };

/* check-ins (browser-local dates), seeded with a few days before today */
const localKey = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const checkins = new Set<string>(
  [1, 2, 3, 4, 5, 8, 9, 12, 33, 34].map((n) => localKey(new Date(Date.now() - n * 86400e3))),
);

let up = 0;
let down = 0;
let updateState: { state: string; version: string; time: number } | null = null;

const LOG = [
  "+0800 2026-11-09 11:06:58 INFO router: sing-box started",
  "+0800 2026-11-09 11:07:02 INFO outbound/anytls[cfg-pp_jp1-out]: connected",
  "+0800 2026-11-09 11:08:40 WARN outbound/anytls[cfg-pp_de1-out]: latency 156 ms",
  "+0800 2026-11-09 11:12:27 WARN dns: https://8.8.8.8/dns-query timeout, fallback",
  "+0800 2026-11-09 11:14:51 ERROR outbound/anytls[cfg-pp_gb1-out]: dial tcp: i/o timeout",
  "+0800 2026-11-09 11:20:05 INFO router: 214 active connections",
].join("\n");

/* payment-v2 demo: 支付宝 -> QR code, 微信支付 -> payment page, USDT -> crypto selection + transfer */
const mockOrder = { no: "", method: 0, polls: 0, status: "pending", amount: "297.60", key: "", network: "", token: "" };
const AMOUNTS: Record<number, string> = { 12: "297.60", 3: "82.80", 1: "29.90" };

function paymentSession() {
  const now = Math.floor(Date.now() / 1000);
  const base = { order_no: mockOrder.no, order_status: mockOrder.status, amount: mockOrder.amount, currency: "CNY", quantity: 1, reused: false };
  if (mockOrder.status !== "pending")
    return { ...base, payment_action: { type: "none", state: mockOrder.status === "paid" ? "paid" : "cancelled" } };
  if (mockOrder.polls < 1)
    return { ...base, payment_action: { type: "preparing", state: "preparing", retry_after_ms: 800 } };
  const common = { state: "ready", expires_at: now + 900, payment_amount: mockOrder.amount, payment_currency: "CNY" };
  if (mockOrder.method === 1)
    return { ...base, payment_action: { ...common, type: "qr", provider: "alipay", qr_content: `https://qr.alipay.com/bax0${mockOrder.no}` } };
  if (mockOrder.method === 2)
    return { ...base, payment_action: { ...common, type: "redirect", provider: "wechat", provider_url: `https://pay.example.com/checkout/${mockOrder.no}`, open_mode: "external" } };
  if (!mockOrder.token)
    return { ...base, payment_action: { ...common, type: "crypto_selection", supported_assets: [
      { network: "tron", display_name: "TRON（TRC20）", tokens: ["USDT"] },
      { network: "ethereum", display_name: "Ethereum（ERC20）", tokens: ["USDT", "USDC"] },
    ] } };
  return { ...base, payment_action: { ...common, type: "crypto_transfer", actual_amount: "41.37", token: mockOrder.token, network: mockOrder.network,
    receive_address: mockOrder.network === "tron" ? "TQ5yWDLRKGfT6vBvXc9yS9HnB8u2QcLmVq" : "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed" } };
}

const ppanel = (path: string, body: Record<string, unknown>, query: Record<string, unknown>, key = "") => {
  const ok = (data: unknown) => ({ ok: true, status: 200, code: 200, msg: "success", data });
  const v2 = path.match(/^\/v2\/orders\/([^/]+)\/(payment|payment\/selection|cancel)$/);
  if (path === "/v2/orders/purchase") {
    /* the same key replays the same order */
    if (key !== mockOrder.key || mockOrder.status !== "pending") {
      Object.assign(mockOrder, { no: `20261109${String(Date.now()).slice(-8)}`, method: Number(body.payment_id), polls: 0, status: "pending",
        amount: AMOUNTS[Number(body.quantity)] ?? "29.90", key, network: "", token: "" });
    }
    return ok(paymentSession());
  }
  if (v2) {
    if (decodeURIComponent(v2[1]) !== mockOrder.no) return { ok: false, status: 200, code: 62027, msg: "payment session invalid" };
    if (v2[2] === "cancel") mockOrder.status = "cancelled";
    else if (v2[2] === "payment/selection") {
      mockOrder.network = String(body.network);
      mockOrder.token = String(body.token);
    } else if (mockOrder.status === "pending" && ++mockOrder.polls >= (mockOrder.method === 3 ? 30 : 6)) mockOrder.status = "paid";
    return ok(paymentSession());
  }
  switch (path) {
    case "/v1/public/user/info":
      return ok({ id: 1, nickname: "Astra345", balance: 3600, commission: 3600, refer_code: "HYPR8K2Q" });
    case "/v1/public/user/subscribe":
      return ok({ list: [{ id: 7, status: 1, start_time: Date.now() - 86400e3 * 30, expire_time: Date.now() + 86400e3 * 366, subscribe: { name: "年度会员", device_limit: 5 } }], total: 1 });
    case "/v1/public/user/devices":
      return ok({ list: [
        { id: 1, current: true, platform: "Linux", ip: "203.0.113.20", online: true, last_login_at: Date.now() - 3600e3 },
        { id: 2, platform: "iOS", ip: "198.51.100.8", online: true, last_login_at: Date.now() - 7200e3 },
        { id: 3, platform: "macOS", ip: "198.51.100.9", online: false, last_login_at: Date.now() - 86400e3 },
      ], total: 3 });
    case "/v1/public/user/device/kick_offline":
      return ok(null);
    case "/v1/public/user/affiliate/count":
      return ok({ registers: 3, total_commission: 3600 });
    case "/v1/public/announcement/list":
      return ok({ announcements: [
        { id: 1, title: "新增 新加坡 02 节点", content: "新加坡 02 已上线。\n路由器会在下次自动同步时获取，也可以在“节点”页手动同步。", created_at: Date.now() - 86400e3, pinned: true },
        { id: 2, title: "11 月 15 日凌晨维护", content: "02:00–04:00 部分节点维护，期间可能短暂断线。", created_at: Date.now() - 4 * 86400e3 },
      ], total: 2 });
    case "/v1/public/subscribe/list":
      return ok({
        units: { amount_scale: 100, currency_symbol: "¥" },
        list: [{
          id: 1, name: "Hyper 会员", unit_time: "Month", device_limit: 5,
          discount: [
            { quantity: 12, sell_price: 29760, original_price: 35880, recommend: true },
            { quantity: 3, sell_price: 8280, original_price: 8970 },
            { quantity: 1, sell_price: 2990 },
          ],
        }],
      });
    case "/v1/public/payment/methods":
      return ok({ list: [{ id: 1, name: "支付宝", platform: "epay" }, { id: 2, name: "微信支付", platform: "epay" }, { id: 3, name: "USDT（TRC20）", platform: "gmpay", description: "链上确认约 1 分钟" }] });
    case "/v1/public/order/pre": {
      const q = Number(body.quantity);
      const price = q === 12 ? 29760 : q === 3 ? 8280 : 2990;
      const coupon = Array.isArray(body.coupons) && body.coupons.length ? 1000 : 0;
      return ok({ price, discount: 0, coupon_discount: coupon, fee_amount: 0, amount: price - coupon });
    }
    default:
      return { ok: false, status: 404, error: `mock: ${path}` };
  }
};

/* calls per method (demo mode only), to check polling in a browser: window.hyperMockCalls */
const calls: Record<string, number> = ((globalThis as { hyperMockCalls?: Record<string, number> }).hyperMockCalls = {});

export async function mockCall(method: string, args: Record<string, unknown>): Promise<unknown> {
  calls[method] = (calls[method] ?? 0) + 1;
  await delay(150);
  switch (method) {
    case "status":
      return {
        ...state,
        update: {
          auto_check: config["update.auto_check"] !== "0",
          checked: lastCheck?.time ?? null,
          latest: lastCheck?.latest ?? null,
          available: !!lastCheck?.latest && newer(lastCheck.latest, state.version ?? ""),
        },
      };
    case "set_enabled":
      state.enabled = !!args.enabled;
      state.running = state.enabled;
      return { result: true, running: state.running };
    case "set_mode":
      state.mode = args.mode as Status["mode"];
      return { result: true };
    case "nodes":
      return { nodes, main_node: state.main_node };
    case "select_node": {
      const n = nodes.find((x) => x.name === args.node);
      if (!n) return { result: false, error: "unknown node" };
      state.main_node = n.name;
      state.main_node_label = n.label;
      return { result: true };
    }
    case "latency":
      await delay(600);
      return state.running ? { result: true, latency } : { result: false, error: "not running" };
    case "sync_subscription":
      await delay(800);
      state.sync = { ...(state.sync as object), ok: true, time: now() } as Status["sync"];
      return { result: true, state: state.sync };
    case "traffic":
      if (!state.running) return { running: false };
      up += 900_000 + Math.random() * 600_000;
      down += 6_000_000 + Math.random() * 5_000_000;
      return {
        running: true, upload_total: up, download_total: down,
        per_source: {
          "192.168.1.23": { upload: 120e6, download: 1.8e9, connections: 14 },
          "192.168.1.31": { upload: 40e6, download: 820e6, connections: 22 },
          "192.168.1.40": { upload: 31e6, download: 540e6, connections: 9 },
        },
      };
    case "send_code":
      return /@/.test(String(args.email)) ? { result: true } : { result: false, error: "illegal email" };
    case "login":
      if (!args.email || !(args.password || args.code)) return { result: false, error: "missing credentials" };
      if (args.code === "000000") return { result: false, msg: "验证码错误" };
      state.signed_in = true;
      state.email = String(args.email);
      return { result: true };
    case "logout":
      state.signed_in = false;
      state.enabled = false;
      state.running = false;
      return { result: true };
    case "api":
      /* rpcd refuses payment-v2 writes without an Idempotency-Key */
      if (args.method === "POST" && String(args.path).startsWith("/v2/orders/") && !/^[A-Za-z0-9._:-]{8,128}$/.test(String(args.idempotency_key ?? "")))
        return { ok: false, error: "idempotency key required" };
      return ppanel(String(args.path), (args.body as Record<string, unknown>) || {}, (args.query as Record<string, unknown>) || {}, String(args.idempotency_key ?? ""));
    case "checkin_status":
      return state.signed_in ? { result: true, dates: [...checkins].sort() } : { result: false, error: "not signed in" };
    case "checkin": {
      await delay(300);
      if (!state.signed_in) return { result: false, error: "not signed in" };
      const date = String(args.date ?? "");
      if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) return { result: false, error: "illegal date" };
      const added = !checkins.has(date);
      checkins.add(date);
      return { result: true, added, dates: [...checkins].sort() };
    }
    case "rules_list":
      return { rules, applies: state.mode !== "global" };
    case "set_rule_enabled": {
      const r = rules.find((x) => x.key === args.key);
      if (r) r.enabled = !!args.enabled;
      return { result: !!r };
    }
    case "acllist_read":
      return { content: lists[String(args.type)] ?? "" };
    case "acllist_write": {
      const lines = String(args.content || "").split("\n").map((l) => l.trim()).filter(Boolean);
      const bad = lines.find((l) => !/^[A-Za-z0-9._-]+$/.test(l));
      if (bad) return { result: false, error: `illegal entry: ${bad}` };
      lists[String(args.type)] = lines.join("\n");
      return { result: true, count: lines.length };
    }
    case "get_config":
      return { config: { ...config }, api_base: state.api_base };
    case "set_config":
      Object.assign(config, args.values as Record<string, string>);
      return { result: true };
    case "set_api_base":
      if (args.api_base && !/^https?:\/\//.test(String(args.api_base))) return { result: false, error: "illegal url" };
      state.api_base = String(args.api_base);
      return { result: true };
    case "check_update": {
      await delay(500);
      lastCheck = { time: now(), latest: MOCK_LATEST };
      return {
        result: true, current: state.version, latest: MOCK_LATEST, update_available: newer(MOCK_LATEST, state.version ?? ""),
        published_at: "2026-11-20T08:00:00Z", notes: "- 节点页支持按延迟排序\n- 修复 IPv6 设备偶尔不走代理",
        url: "https://github.com/HyperPteLtd/openwrt-hyper/releases/tag/v0.2.0", installable: true, package_manager: "opkg",
        update_state: updateState,
      };
    }
    case "install_update":
      updateState = { state: "installing", version: "0.2.0", time: now() };
      setTimeout(() => {
        updateState = { state: "done", version: "0.2.0", time: now() };
        state.version = "0.2.0-r1";
      }, 4000);
      return { result: true, started: true, version: "0.2.0" };
    case "logs":
      return { content: args.type === "hyper" ? "2026-11-09 11:15:02 [SYNC] 9 nodes from 7 countries (1 added, 0 removed, 0 invalid links).\n2026-11-09 11:15:03 [SYNC] Rules: 4 lists." : LOG };
    case "log_clean":
      return { result: true };
    case "diag":
      await delay(700);
      if (!/^[A-Za-z0-9][A-Za-z0-9.:-]*$/.test(String(args.target))) return { result: false, error: "illegal target" };
      return { result: true, output: `PING ${args.target}: 4 packets transmitted, 4 received, 0% loss\nround-trip min/avg/max = 45.1/46.8/48.2 ms` };
    case "health_check":
      await delay(900);
      return { service: { ok: state.running }, node: state.running ? { delay: 46 } : null, dns: { ok: true }, direct: { ok: true, ms: 12 }, proxy: { ok: state.running, ms: 210 } };
    case "lan_clients":
      return { clients, lan_proxy_mode: access.mode, direct_macs: access.direct };
    case "set_access_control":
      access = { mode: args.mode === "pick" ? "except_listed" : "disabled", direct: (args.direct_macs as string[]) || [] };
      return { result: true };
    default:
      return { result: false, error: `mock: ${method} not implemented` };
  }
}
