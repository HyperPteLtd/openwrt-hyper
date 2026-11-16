import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { app } from "../lib/app";
import { formatAgo, latencyTone } from "../lib/format";
import { serviceText } from "../lib/display";
import { operationPending } from "../lib/service";
import { nodeLabel, nodeRegion, regionFlag, regionName } from "../lib/regions";
import { beginNodeMeasurement, call, readNodeLatency } from "../lib/rpc";
import type { NodeMeasurements } from "../lib/node-latency";
import type { NodeInfo, NodesResult, Result, SyncState } from "../lib/types";
import { Card } from "./ui";

const TONE: Record<string, string> = { good: "var(--good)", warn: "var(--warn)", none: "#8e8e93" };

export function NodeList() {
  const nodes = useSignal<NodeInfo[]>([]);
  const latency = useSignal<NodeMeasurements>({});
  const query = useSignal("");
  const busy = useSignal("");
  const message = useSignal("");
  const loadId = useRef(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  const load = async () => {
    const id = ++loadId.current;
    const apply = (r: NodesResult) => {
      if (id !== loadId.current) return;
      nodes.value = r.nodes || [];
      latency.value = readNodeLatency(nodes.value);
    };
    try {
      apply(await call<NodesResult>("nodes", {}, { onUpdate: apply }));
    } catch (e) {
      if (id === loadId.current) message.value = (e as Error).message;
    }
  };

  const tick = app.tick;
  const identity = app.status?.signed_in ? app.status.email || "signed-in" : "signed-out";
  const loadedIdentity = useRef(identity);
  useEffect(() => {
    if (loadedIdentity.current !== identity) {
      loadedIdentity.current = identity;
      nodes.value = [];
      latency.value = {};
      message.value = "";
    }
    load();
    return () => { loadId.current++; };
  }, [tick, identity]);

  const measure = async () => {
    busy.value = "latency";
    message.value = "";
    const remember = beginNodeMeasurement();
    try {
      const r = await call<Result & { latency?: Record<string, number | null> }>("latency");
      if (r.result && r.latency) {
        const snapshot = remember(r.latency);
        if (snapshot && mounted.current) {
          latency.value = snapshot;
          return r.latency;
        }
      } else if (mounted.current) message.value = r.error === "not running" ? tr("Hyper 运行时才能测速") : r.error || tr("测速失败");
    } catch (e) {
      if (mounted.current) message.value = (e as Error).message;
    } finally {
      if (mounted.current) busy.value = "";
    }
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
    const measured = await measure();
    if (!measured) return;
    const names = new Set(nodes.value.map((node) => node.name));
    const best = Object.entries(measured)
      .filter(([name, ms]) => names.has(name) && ms != null && ms > 0)
      .sort((a, b) => (a[1] as number) - (b[1] as number))[0];
    if (best) await select(best[0]);
  };

  const q = query.value.trim().toLowerCase();
  const list = nodes.value.filter(
    (n) => !q || [n.label, nodeLabel(n.label), regionName(nodeRegion(n)), n.country, n.city].some((text) => text?.toLowerCase().includes(q)),
  );
  const current = app.status?.main_node;
  const isBusy = !app.liveStatus || !!busy.value || !!app.controlAction || operationPending(app.status?.operation);

  return (
    <Card id="overview-nodes" className="node-section" label={tr("节点")}>
      <div class="node-toolbar">
        <div class="node-heading">
          <h2>{tr("选择节点")}</h2>
          <p class="muted small">{tr("共 {0} 个节点 · {1}更新", [nodes.value.length, formatAgo(app.status?.sync?.time)])}</p>
        </div>
        <div class="node-actions">
          <label class="node-search">
            <span class="muted" aria-hidden="true">⌕</span>
            <input
              type="search"
              aria-label={tr("搜索节点")}
              placeholder={tr("搜索国家、地区或城市")}
              value={query.value} onInput={(e) => (query.value = e.currentTarget.value)}
              style={{ all: "unset", flexGrow: 1, fontSize: "var(--control-font-size)" }}
            />
          </label>
          <button type="button" class="btn" disabled={isBusy} onClick={() => measure()}>{busy.value === "latency" ? tr("测速中…") : tr("刷新延迟")}</button>
          <button type="button" class="btn" disabled={isBusy} onClick={() => sync()}>{busy.value === "sync" ? tr("更新中…") : tr("更新节点")}</button>
          <button type="button" class="btn primary" disabled={isBusy || !app.status?.running} onClick={() => autoPick()}>{tr("自动选择")}</button>
        </div>
      </div>

      {message.value && <div class="banner">{serviceText(message.value)}</div>}

      <div class="node-list">
        {list.length ? (
          <ul class="node-grid">
            {list.map((n) => {
              const measurement = latency.value[n.name];
              const ms = measurement?.ms;
              const tone = latencyTone(ms);
              const on = n.name === current;
              const region = nodeRegion(n);
              return (
                <li key={n.name}>
                  <button
                    type="button"
                    class="option node-option"
                    aria-pressed={on}
                    disabled={isBusy}
                    onClick={() => select(n.name)}
                  >
                    <span aria-hidden="true" class="node-flag">{regionFlag(region)}</span>
                    <span class="node-name">
                      <span>{nodeLabel(n.label)}</span>
                      {on && <span class="node-current">{tr("当前")}</span>}
                    </span>
                    <span class="node-latency" title={measurement ? formatAgo(Math.floor(measurement.measuredAt / 1000)) : undefined} style={{ color: TONE[tone] }}>
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
      </div>
    </Card>
  );
}
