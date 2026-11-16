import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { usePoll } from "../lib/poll";
import { CardHeader, Notice, Switch } from "../components/ui";
import { app } from "../lib/app";
import { formatBytes } from "../lib/format";
import { call } from "../lib/rpc";
import type { Traffic } from "../lib/types";
import { useAutosave } from "../components/AutosaveStatus";
import { deviceWrites, type DevicePreferences } from "../lib/preferences";
import "./Devices.css";

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

export function Devices({ embedded = false }: { embedded?: boolean }) {
  const clients = useSignal<Client[]>([]);
  const mode = useSignal<"all" | "pick">("all");
  const direct = useSignal<string[]>([]);
  const loaded = useSignal(false);
  const traffic = useSignal<Traffic["per_source"]>({});
  const msg = useSignal("");
  const active = useRef(true);
  const requestSequence = useRef(0);
  useAutosave(deviceWrites);

  useEffect(() => {
    active.current = true;
    return () => { active.current = false; requestSequence.current++; };
  }, []);

  const load = async () => {
    const request = ++requestSequence.current;
    const current = () => active.current && request === requestSequence.current;
    const revision = deviceWrites.revision;
    const pendingAtRead = deviceWrites.value;
    const apply = (r: LanClients) => {
      if (!current()) return;
      clients.value = [...(r.clients || [])].sort((a, b) => (a.ip || "").localeCompare(b.ip || "", undefined, { numeric: true }));
      // A background refresh can update clients, but must not undo a local edit.
      if (revision === deviceWrites.revision) {
        const pending = deviceWrites.value ?? pendingAtRead;
        mode.value = pending?.mode ?? (r.lan_proxy_mode === "except_listed" ? "pick" : "all");
        direct.value = pending?.direct_macs ?? r.direct_macs ?? [];
      }
      loaded.value = true;
      msg.value = "";
    };
    try {
      const r = await call<LanClients>("lan_clients", {}, { onUpdate: apply });
      apply(r);
      if (!current()) return;
      const t = await call<Traffic>("traffic");
      if (current()) traffic.value = t.per_source || {};
    } catch (e) {
      if (current()) msg.value = (e as Error).message;
    }
  };
  /* devices come and go and their traffic grows: refresh every 10 s while visible, and after saving */
  usePoll(load, 10000);
  const tick = app.tick;
  const mountTick = useRef(tick);
  useEffect(() => {
    if (tick !== mountTick.current) load();
  }, [tick]);

  const save = () => {
    deviceWrites.enqueue({ mode: mode.value, direct_macs: [...direct.value] });
  };
  const chooseMode = (value: DevicePreferences["mode"]) => {
    if (mode.value === value) return;
    mode.value = value;
    save();
  };
  const toggle = (mac: string, proxied: boolean) => {
    direct.value = proxied ? direct.value.filter((m) => m !== mac) : [...new Set([...direct.value, mac])];
    save();
  };
  const disabled = !loaded.value || !app.liveStatus;

  const proxiedCount = clients.value.filter((c) => mode.value === "all" || !direct.value.includes(c.mac)).length;
  const clientCount = clients.value.length;
  const Container = embedded ? "div" : "main";

  return (
    <Container class={embedded ? "devices-view devices-embedded" : "page devices-view"} aria-label={tr("接入设备")}>
      <Notice text={msg.value} />
      <section class={embedded ? "devices-panel" : "card devices-panel"} aria-busy={!loaded.value}>
        <CardHeader title={tr("接入设备")} desc={tr("{0} 台走代理 · {1} 台直连", [proxiedCount, clientCount - proxiedCount])}>
          <div class="segmented device-range" role="radiogroup" aria-label={tr("哪些设备走代理")}>
            {([{ id: "all", label: tr("全部设备") }, { id: "pick", label: tr("按设备选择") }] as const).map((option) => (
              <button key={option.id} type="button" class="btn" role="radio" data-mode={option.id} aria-checked={mode.value === option.id} tabIndex={disabled || mode.value !== option.id ? -1 : 0} disabled={disabled} onClick={() => chooseMode(option.id)} onKeyDown={(e) => {
                const next = e.key === "Home" ? "all" : e.key === "End" ? "pick" : ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key) ? option.id === "all" ? "pick" : "all" : null;
                if (!next) return;
                e.preventDefault(); chooseMode(next);
                e.currentTarget.parentElement?.querySelector<HTMLButtonElement>(`[data-mode="${next}"]`)?.focus();
              }}>{option.label}</button>
            ))}
          </div>
        </CardHeader>
        <div class="device-table-scroll" role="region" aria-label={tr("局域网设备 · {0} 台", [clientCount])} tabIndex={0}>
          <table class="device-table" aria-label={tr("接入设备")}>
            <colgroup><col /><col /><col /><col /></colgroup>
            <thead>
              <tr>
                <th scope="col">{tr("设备")}</th>
                <th scope="col">IP / MAC</th>
                <th scope="col" title={tr("“当前连接流量”是这台设备正在进行的连接累计的流量")}>{tr("当前连接流量")}</th>
                <th scope="col">{tr("走代理")}</th>
              </tr>
            </thead>
            <tbody>
              {clients.value.map((c) => {
                const proxied = mode.value === "all" || !direct.value.includes(c.mac);
                const t = traffic.value?.[c.ip];
                return (
                  <tr key={c.mac}>
                    <td><span class="device-identity"><span class="device-name">{c.name || tr("未命名设备")}</span><span class={`device-online ${c.online ? "is-online" : ""}`}>{c.online ? tr("在线") : tr("离线")}</span></span></td>
                    <td><span class="device-address"><span>{c.ip}</span><span class="muted">{c.mac}</span></span></td>
                    <td class="device-traffic">{t ? formatBytes(t.download + t.upload) : "--"}</td>
                    <td><Switch label={tr("{0} 走代理", [c.name || c.ip])} checked={proxied} disabled={disabled || mode.value === "all"} onToggle={(on) => toggle(c.mac, on)} /></td>
                  </tr>
                );
              })}
              {!clients.value.length && <tr><td colSpan={4} class="muted device-empty">{loaded.value ? tr("没有发现设备") : tr("加载中…")}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </Container>
  );
}
