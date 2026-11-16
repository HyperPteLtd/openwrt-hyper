import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { app } from "../lib/app";
import { formatAgo, latencyTone } from "../lib/format";
import { serviceText } from "../lib/display";
import { acknowledgeNode, nodeSelection, selectNode } from "../lib/node-selection";
import { NodeProbe, type ProbeProgress, type ProbeReply } from "../lib/node-probe";
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
  const syncing = useSignal(false);
  const probing = useSignal<ProbeProgress>({ running: false, completed: 0, total: 0 });
  const message = useSignal("");
  const loadId = useRef(0);
  const mounted = useRef(true);
  const probe = useRef<NodeProbe>();
  if (!probe.current) probe.current = new NodeProbe((method, args) => call<ProbeReply>(method, args), value => {
    if (mounted.current) probing.value = value;
  });
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; probe.current!.cancel(); };
  }, []);

  const load = async (fresh = false) => {
    const id = ++loadId.current;
    const apply = (r: NodesResult) => {
      if (id !== loadId.current) return;
      nodes.value = r.nodes || [];
      acknowledgeNode(r.main_node);
      latency.value = readNodeLatency(nodes.value);
    };
    try {
      apply(await call<NodesResult>("nodes", {}, { fresh, onUpdate: apply }));
    } catch (e) {
      if (id === loadId.current) message.value = (e as Error).message;
    }
  };

  const tick = app.tick;
  const identity = app.status?.signed_in ? app.status.email || "signed-in" : "signed-out";
  const loadedIdentity = useRef(identity);
  const syncTime = app.status?.sync?.time;
  const loadedSyncTime = useRef(syncTime);
  useEffect(() => {
    if (loadedIdentity.current !== identity) {
      probe.current!.cancel();
      loadedIdentity.current = identity;
      nodes.value = [];
      latency.value = {};
      message.value = "";
    }
    const fresh = loadedSyncTime.current !== syncTime;
    loadedSyncTime.current = syncTime;
    load(fresh);
    return () => { loadId.current++; };
  }, [tick, identity, syncTime]);

  const intentRevision = app.intentRevision;
  useEffect(() => { probe.current!.cancel(); }, [intentRevision]);

  const measure = async () => {
    message.value = "";
    const remember = beginNodeMeasurement();
    try {
      const result = await probe.current!.run(nodes.value.map(node => node.name), values => {
        const snapshot = remember(values);
        if (snapshot && mounted.current) latency.value = snapshot;
        else if (!snapshot) probe.current!.cancel();
      });
      return remember({}) ? result : undefined;
    } catch (error) {
      if (mounted.current) message.value = tr("测速失败");
    }
  };

  const sync = async () => {
    app.intentRevision++;
    probe.current!.cancel();
    syncing.value = true;
    message.value = "";
    try {
      type SyncReply = { result: boolean; pending?: boolean; state: SyncState | null };
      let r = await call<SyncReply>("sync_subscription");
      while (r.pending && mounted.current) {
        await new Promise(resolve => setTimeout(resolve, 500));
        if (!mounted.current) return;
        r = await call<SyncReply>("sync_status");
      }
      if (!r.result) message.value = tr("更新失败：{0}", [r.state?.msg || r.state?.error || tr("未知原因")]);
      await load(true);
    } catch (e) {
      message.value = (e as Error).message;
    }
    syncing.value = false;
    app.tick++;
  };

  const select = (name: string) => {
    probe.current!.cancel();
    message.value = "";
    selectNode(name);
  };

  const autoPick = async () => {
    const intent = app.intentRevision;
    const measured = await measure();
    if (!measured || intent !== app.intentRevision || !mounted.current) return;
    const names = new Set(nodes.value.map((node) => node.name));
    const best = Object.entries(measured)
      .filter(([name, ms]) => names.has(name) && ms != null && ms > 0)
      .sort((a, b) => (a[1] as number) - (b[1] as number))[0];
    if (best) select(best[0]);
  };

  const q = query.value.trim().toLowerCase();
  const list = nodes.value.filter(
    (n) => !q || [n.label, nodeLabel(n.label), regionName(nodeRegion(n)), n.country, n.city].some((text) => text?.toLowerCase().includes(q)),
  );
  const current = nodeSelection.value.desired || app.status?.main_node;
  const disabled = !app.liveStatus;

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
          <button type="button" class="btn" disabled={disabled || !nodes.value.length} onClick={() => probing.value.running ? probe.current!.cancel() : measure()}>{probing.value.running ? tr("取消测速") : tr("刷新延迟")}</button>
          <button type="button" class="btn" disabled={disabled || syncing.value} onClick={() => sync()}>{syncing.value ? tr("更新中…") : tr("更新节点")}</button>
          <button type="button" class="btn primary" disabled={disabled || !nodes.value.length} onClick={() => autoPick()}>{tr("自动选择")}</button>
        </div>
      </div>

      {probing.value.running && <p class="muted small" role="status">{tr("测速中 {0}/{1}", [probing.value.completed, probing.value.total])}</p>}
      {(message.value || nodeSelection.value.error) && <div class="banner">{serviceText(message.value || nodeSelection.value.error)}</div>}

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
                    disabled={disabled}
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
