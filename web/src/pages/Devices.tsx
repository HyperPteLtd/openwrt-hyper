import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { usePoll } from "../lib/poll";
import { Notice, PageTitle, Switch } from "../components/ui";
import { app } from "../lib/app";
import { formatBytes } from "../lib/format";
import { call } from "../lib/rpc";
import type { Result, Traffic } from "../lib/types";

interface Client {
  mac: string;
  ip: string;
  name: string | null;
  online?: boolean;
}

interface LanClients {
  clients: Client[];
  lan_proxy_mode: "disabled" | "listed_only" | "except_listed";
  direct_macs: string[];
}

export function Devices() {
  const clients = useSignal<Client[]>([]);
  const mode = useSignal<"all" | "pick">("all");
  const direct = useSignal<string[]>([]);
  const savedKey = useSignal("");
  const traffic = useSignal<Traffic["per_source"]>({});
  const busy = useSignal(false);
  const msg = useSignal("");
  const ok = useSignal("");

  const load = async () => {
    try {
      const r = await call<LanClients>("lan_clients");
      clients.value = [...(r.clients || [])].sort((a, b) => (a.ip || "").localeCompare(b.ip || "", undefined, { numeric: true }));
      /* only refresh the editable state when nothing is pending */
      const key = JSON.stringify([r.lan_proxy_mode, [...(r.direct_macs || [])].sort()]);
      if (!savedKey.value || savedKey.value === JSON.stringify([mode.value === "pick" ? "except_listed" : "disabled", [...direct.value].sort()])) {
        mode.value = r.lan_proxy_mode === "except_listed" ? "pick" : "all";
        direct.value = r.direct_macs || [];
      }
      savedKey.value = key;
      const t = await call<Traffic>("traffic");
      traffic.value = t.per_source || {};
    } catch (e) {
      msg.value = (e as Error).message;
    }
  };
  /* devices come and go and their traffic grows: refresh every 10 s while visible, and after saving */
  usePoll(load, 10000);
  const tick = app.tick;
  const mountTick = useRef(tick);
  useEffect(() => {
    if (tick !== mountTick.current) load();
  }, [tick]);

  const currentKey = JSON.stringify([mode.value === "pick" ? "except_listed" : "disabled", [...direct.value].sort()]);
  const dirty = !!savedKey.value && currentKey !== savedKey.value && !(mode.value === "pick" && direct.value.length === 0 && savedKey.value.startsWith('["disabled"'));

  const toggle = (mac: string, proxied: boolean) => {
    direct.value = proxied ? direct.value.filter((m) => m !== mac) : [...direct.value, mac];
  };

  const save = async () => {
    busy.value = true;
    msg.value = "";
    ok.value = "";
    try {
      const r = await call<Result>("set_access_control", { mode: mode.value, direct_macs: mode.value === "pick" ? direct.value : [] });
      if (r.result) {
        ok.value = "已保存，规则已生效。";
        savedKey.value = currentKey;
      } else msg.value = r.error || "保存失败";
    } catch (e) {
      msg.value = (e as Error).message;
    }
    busy.value = false;
    app.tick++;
  };

  const proxiedCount = clients.value.filter((c) => mode.value === "all" || !direct.value.includes(c.mac)).length;
  const saving = busy.value;
  const clientCount = clients.value.length;

  return (
    <main class="page">
      <PageTitle title="接入设备" sub="连到这台路由器的设备都可以经由 Hyper 上网。你可以让个别设备保持直连，比如游戏机或公司电脑。" />
      <Notice text={msg.value} />
      <Notice text={ok.value} tone="ok" />

      <div class="row" style={{ alignItems: "flex-start" }}>
        <section class="card" style={{ flex: "1 1 300px" }}>
          <h2>哪些设备走代理</h2>
          <div role="radiogroup" aria-label="哪些设备走代理" style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
            <button type="button" role="radio" class="option" aria-checked={mode.value === "all"} onClick={() => (mode.value = "all")}>
              <span class="grow">
                <span style={{ fontSize: "15px" }}>全部设备</span>
                <span class="muted small">局域网所有设备都经过代理</span>
              </span>
            </button>
            <button type="button" role="radio" class="option" aria-checked={mode.value === "pick"} onClick={() => (mode.value = "pick")}>
              <span class="grow">
                <span style={{ fontSize: "15px" }}>按设备选择</span>
                <span class="muted small">在右侧逐台开关，新设备默认走代理</span>
              </span>
            </button>
          </div>
          <button type="button" class="btn primary" disabled={!dirty || saving} onClick={() => save()}>
            {saving ? "保存中…" : dirty ? "保存并应用" : "已保存"}
          </button>
        </section>

        <section class="card" style={{ flex: "2 1 520px" }}>
          <div class="card-head" style={{ flexWrap: "wrap" }}>
            <h2>局域网设备 · {clientCount} 台</h2>
            <span class="muted small">{proxiedCount} 台走代理 · {clientCount - proxiedCount} 台直连</span>
          </div>
          <div style={{ overflowX: "auto" }}>
            <table style={{ width: "100%", minWidth: "520px", borderCollapse: "collapse" }}>
              <thead>
                <tr class="muted small" style={{ textAlign: "left" }}>
                  <th scope="col" style={{ fontWeight: 500, padding: "8px 4px" }}>设备</th>
                  <th scope="col" style={{ fontWeight: 500, padding: "8px 4px" }}>IP / MAC</th>
                  <th scope="col" style={{ fontWeight: 500, padding: "8px 4px", textAlign: "right" }}>当前连接流量</th>
                  <th scope="col" style={{ fontWeight: 500, padding: "8px 4px", textAlign: "right" }}>走代理</th>
                </tr>
              </thead>
              <tbody>
                {clients.value.map((c) => {
                  const proxied = mode.value === "all" || !direct.value.includes(c.mac);
                  const t = traffic.value?.[c.ip];
                  return (
                    <tr key={c.mac} style={{ borderTop: "1px solid var(--line)" }}>
                      <td style={{ padding: "12px 4px" }}>
                        <span style={{ display: "flex", flexDirection: "column", gap: "2px" }}>
                          <span>{c.name || "未命名设备"}</span>
                          <span class="muted small">{c.online ? "在线" : "离线"}</span>
                        </span>
                      </td>
                      <td class="mono small" style={{ padding: "12px 4px", color: "#c8c8cc" }}>
                        <span style={{ display: "flex", flexDirection: "column", gap: "2px" }}>
                          <span>{c.ip}</span>
                          <span class="muted">{c.mac}</span>
                        </span>
                      </td>
                      <td style={{ padding: "12px 4px", textAlign: "right", color: "#c8c8cc", fontVariantNumeric: "tabular-nums" }}>
                        {t ? formatBytes(t.download + t.upload) : "--"}
                      </td>
                      <td style={{ padding: "12px 4px", textAlign: "right" }}>
                        <Switch label={`${c.name || c.ip} 走代理`} checked={proxied} disabled={mode.value === "all"} onToggle={(on) => toggle(c.mac, on)} />
                      </td>
                    </tr>
                  );
                })}
                {!clients.value.length && (
                  <tr>
                    <td colSpan={4} class="muted" style={{ padding: "24px 4px" }}>没有发现设备。</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <span class="muted small">“当前连接流量”是这台设备正在进行的连接累计的流量，来自 sing-box clash_api。</span>
        </section>
      </div>
    </main>
  );
}
