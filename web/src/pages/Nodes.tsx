import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { app } from "../lib/app";
import { formatAgo, latencyTone } from "../lib/format";
import { call } from "../lib/rpc";
import type { NodeInfo, NodesResult, Result, SyncState } from "../lib/types";

const TONE: Record<string, string> = { good: "var(--good)", warn: "var(--warn)", none: "#8e8e93" };

/* sing-box protocol / transport names as people know them */
const PROTOCOL: Record<string, string> = {
  anytls: "AnyTLS", vless: "VLESS", vmess: "VMess", trojan: "Trojan", shadowsocks: "Shadowsocks",
  hysteria: "Hysteria", hysteria2: "Hysteria2", tuic: "TUIC", wireguard: "WireGuard", socks: "SOCKS", http: "HTTP",
};
const TRANSPORT: Record<string, string> = { ws: "WebSocket", grpc: "gRPC", http: "HTTP/2", httpupgrade: "HTTPUpgrade", quic: "QUIC" };

export function Nodes() {
  const nodes = useSignal<NodeInfo[]>([]);
  const latency = useSignal<Record<string, number | null>>({});
  const query = useSignal("");
  const busy = useSignal("");
  const message = useSignal("");

  const load = async () => {
    try {
      const r = await call<NodesResult>("nodes");
      nodes.value = r.nodes || [];
    } catch (e) {
      message.value = (e as Error).message;
    }
  };

  const tick = app.tick;
  useEffect(() => {
    load();
  }, [tick]);

  const measure = async () => {
    busy.value = "latency";
    message.value = "";
    try {
      const r = await call<Result & { latency?: Record<string, number | null> }>("latency");
      if (r.result && r.latency) latency.value = r.latency;
      else message.value = r.error === "not running" ? "Hyper 运行时才能测速。" : r.error || "测速失败";
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = "";
  };

  const sync = async () => {
    busy.value = "sync";
    message.value = "";
    try {
      const r = await call<{ result: boolean; state: SyncState | null }>("sync_subscription");
      if (!r.result) message.value = `同步失败：${r.state?.msg || r.state?.error || "未知原因"}`;
      await load();
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = "";
    app.tick++;
  };

  const select = async (name: string) => {
    busy.value = name;
    try {
      const r = await call<Result>("select_node", { node: name });
      if (!r.result) message.value = r.error || "切换失败";
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = "";
    app.tick++;
  };

  const autoPick = async () => {
    await measure();
    const best = Object.entries(latency.value)
      .filter(([, ms]) => ms != null && ms > 0)
      .sort((a, b) => (a[1] as number) - (b[1] as number))[0];
    if (best) await select(best[0]);
  };

  const q = query.value.trim().toLowerCase();
  const list = nodes.value.filter(
    (n) => !q || n.label.toLowerCase().includes(q) || (n.country || "").toLowerCase().includes(q) || (n.city || "").toLowerCase().includes(q),
  );
  const current = app.status?.main_node;

  return (
    <main class="page">
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: "16px" }}>
        <div style={{ flex: "1 1 280px", display: "flex", flexDirection: "column", gap: "6px" }}>
          <h2 style={{ margin: 0, fontSize: "26px", fontWeight: 600 }}>选择你的下一站</h2>
          <p class="muted" style={{ margin: 0 }}>
            共 {nodes.value.length} 个节点，来自你的订阅 · {formatAgo(app.status?.sync?.time)}同步
          </p>
        </div>
        <div style={{ display: "flex", flexWrap: "wrap", gap: "10px", alignItems: "center" }}>
          <label style={{ display: "flex", alignItems: "center", gap: "8px", minHeight: "44px", padding: "0 16px", borderRadius: "22px", background: "var(--card)", minWidth: "220px" }}>
            <span class="muted" aria-hidden="true">⌕</span>
            <input
              type="search"
              aria-label="搜索节点"
              placeholder="搜索国家、地区或城市"
              value={query.value} onInput={(e) => (query.value = e.currentTarget.value)}
              style={{ all: "unset", flexGrow: 1, fontSize: "14px" }}
            />
          </label>
          <button type="button" class="btn" disabled={!!busy.value} onClick={() => measure()}>{busy.value === "latency" ? "测速中…" : "刷新延迟"}</button>
          <button type="button" class="btn" disabled={!!busy.value} onClick={() => sync()}>{busy.value === "sync" ? "同步中…" : "同步订阅"}</button>
          <button type="button" class="btn primary" disabled={!!busy.value || !app.status?.running} onClick={() => autoPick()}>自动选择</button>
        </div>
      </div>

      {message.value && <div class="banner">{message.value}</div>}

      <section class="card" style={{ padding: "8px 12px" }}>
        {list.length ? (
          <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
            {list.map((n) => {
              const ms = latency.value[n.name];
              const tone = latencyTone(ms);
              const on = n.name === current;
              return (
                <li key={n.name}>
                  <button
                    type="button"
                    class="option"
                    aria-pressed={on}
                    disabled={!!busy.value}
                    onClick={() => select(n.name)}
                    style={{ width: "100%", border: 0, background: on ? "var(--control)" : "transparent", minHeight: "64px" }}
                  >
                    <span class="grow">
                      <span style={{ fontSize: "15px", fontWeight: 600 }}>{n.label}</span>
                      <span class="muted small">
                        {[PROTOCOL[n.type] ?? n.type, n.reality ? "REALITY" : "", n.transport ? (TRANSPORT[n.transport] ?? n.transport) : "", on ? "当前" : ""].filter(Boolean).join(" · ")}
                      </span>
                    </span>
                    <span style={{ minWidth: "64px", textAlign: "right", color: TONE[tone], fontVariantNumeric: "tabular-nums" }}>
                      {ms === undefined ? "" : tone === "none" ? "超时" : `${ms} ms`}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        ) : (
          <div class="empty">
            <b style={{ fontSize: "17px" }}>{nodes.value.length ? "没有找到匹配的节点" : "还没有节点"}</b>
            <span class="muted">{nodes.value.length ? "换个关键词试试，比如国家或城市名称。" : "登录后点“同步订阅”拉取节点。"}</span>
            {nodes.value.length > 0 && (
              <button type="button" class="btn" onClick={() => (query.value = "")}>清除搜索</button>
            )}
          </div>
        )}
      </section>
    </main>
  );
}
