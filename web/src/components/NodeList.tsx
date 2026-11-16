import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { app } from "../lib/app";
import { formatAgo, latencyTone } from "../lib/format";
import { serviceText } from "../lib/display";
import { operationPending } from "../lib/service";
import { nodeLabel, nodeRegion, regionFlag, regionName } from "../lib/regions";
import { call } from "../lib/rpc";
import type { NodeInfo, NodesResult, Result, SyncState } from "../lib/types";

const TONE: Record<string, string> = { good: "var(--good)", warn: "var(--warn)", none: "#8e8e93" };

export function NodeList() {
  const nodes = useSignal<NodeInfo[]>([]);
  const latency = useSignal<Record<string, number | null>>({});
  const query = useSignal("");
  const busy = useSignal("");
  const message = useSignal("");
  const loadId = useRef(0);

  const load = async () => {
    const id = ++loadId.current;
    const apply = (r: NodesResult) => { if (id === loadId.current) nodes.value = r.nodes || []; };
    try {
      apply(await call<NodesResult>("nodes", {}, { onUpdate: apply }));
    } catch (e) {
      if (id === loadId.current) message.value = (e as Error).message;
    }
  };

  const tick = app.tick;
  useEffect(() => {
    load();
    return () => { loadId.current++; };
  }, [tick]);

  const measure = async () => {
    busy.value = "latency";
    message.value = "";
    try {
      const r = await call<Result & { latency?: Record<string, number | null> }>("latency");
      if (r.result && r.latency) latency.value = r.latency;
      else message.value = r.error === "not running" ? tr("Hyper 运行时才能测速") : r.error || tr("测速失败");
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
      if (!r.result) message.value = tr("更新失败：{0}", [r.state?.msg || r.state?.error || tr("未知原因")]);
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
      if (!r.result) message.value = r.error || tr("切换失败");
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
    (n) => !q || [n.label, nodeLabel(n.label), regionName(nodeRegion(n)), n.country, n.city].some((text) => text?.toLowerCase().includes(q)),
  );
  const current = app.status?.main_node;
  const isBusy = !!busy.value || !!app.controlAction || operationPending(app.status?.operation);

  return (
    <section id="overview-nodes" aria-label={tr("节点")} style={{ display: "flex", flexDirection: "column", gap: "16px", scrollMarginTop: "20px" }}>
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: "16px" }}>
        <div style={{ flex: "1 1 280px", display: "flex", flexDirection: "column", gap: "6px" }}>
          <h2 style={{ margin: 0, fontSize: "22px", fontWeight: 600 }}>{tr("选择节点")}</h2>
          <p class="muted" style={{ margin: 0 }}>{tr("共 {0} 个节点 · {1}更新", [nodes.value.length, formatAgo(app.status?.sync?.time)])}</p>
        </div>
        <div style={{ display: "flex", flexWrap: "wrap", gap: "10px", alignItems: "center" }}>
          <label style={{ display: "flex", alignItems: "center", gap: "8px", minHeight: "44px", padding: "0 16px", borderRadius: "22px", background: "var(--card)", minWidth: "220px" }}>
            <span class="muted" aria-hidden="true">⌕</span>
            <input
              type="search"
              aria-label={tr("搜索节点")}
              placeholder={tr("搜索国家、地区或城市")}
              value={query.value} onInput={(e) => (query.value = e.currentTarget.value)}
              style={{ all: "unset", flexGrow: 1, fontSize: "14px" }}
            />
          </label>
          <button type="button" class="btn" disabled={isBusy} onClick={() => measure()}>{busy.value === "latency" ? tr("测速中…") : tr("刷新延迟")}</button>
          <button type="button" class="btn" disabled={isBusy} onClick={() => sync()}>{busy.value === "sync" ? tr("更新中…") : tr("更新节点")}</button>
          <button type="button" class="btn primary" disabled={isBusy || !app.status?.running} onClick={() => autoPick()}>{tr("自动选择")}</button>
        </div>
      </div>

      {message.value && <div class="banner">{serviceText(message.value)}</div>}

      <section class="card" style={{ padding: "8px 12px" }}>
        {list.length ? (
          <ul style={{ listStyle: "none", margin: 0, padding: 0, maxHeight: "420px", overflowY: "auto" }}>
            {list.map((n) => {
              const ms = latency.value[n.name];
              const tone = latencyTone(ms);
              const on = n.name === current;
              const region = nodeRegion(n);
              return (
                <li key={n.name}>
                  <button
                    type="button"
                    class="option"
                    aria-pressed={on}
                    disabled={isBusy}
                    onClick={() => select(n.name)}
                    style={{ width: "100%", border: 0, background: on ? "var(--control)" : "transparent", minHeight: "64px" }}
                  >
                    <span aria-hidden="true" style={{ fontSize: "26px", minWidth: "36px", flexShrink: 0 }}>{regionFlag(region)}</span>
                    <span class="grow" style={{ minWidth: 0 }}>
                      <span style={{ fontSize: "15px", fontWeight: 600, overflowWrap: "anywhere" }}>{nodeLabel(n.label)}</span>
                      {on && <span class="muted small">{tr("当前")}</span>}
                    </span>
                    <span style={{ minWidth: "64px", textAlign: "right", color: TONE[tone], fontVariantNumeric: "tabular-nums" }}>
                      {ms === undefined ? "" : tone === "none" ? tr("超时") : `${ms} ms`}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        ) : (
          <div class="empty">
            <b style={{ fontSize: "17px" }}>{nodes.value.length ? tr("没有找到匹配的节点") : tr("还没有节点")}</b>
            <span class="muted">{nodes.value.length ? tr("换个关键词试试，比如国家或城市名称") : tr("登录后点“更新节点”获取节点")}</span>
            {nodes.value.length > 0 && (
              <button type="button" class="btn" onClick={() => (query.value = "")}>{tr("清除搜索")}</button>
            )}
          </div>
        )}
      </section>
    </section>
  );
}
