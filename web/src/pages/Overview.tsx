import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { app } from "../lib/app";
import { usePoll } from "../lib/poll";
import { formatBytes, formatUptime } from "../lib/format";
import { call } from "../lib/rpc";
import { serviceText } from "../lib/display";
import { Announcements } from "../components/Announcements";
import { Card, Row } from "../components/ui";
import { useAutosave } from "../components/AutosaveStatus";
import { configWrites } from "../lib/preferences";
import { desiredEnabled, requestEnabled, requestMode } from "../lib/controls";
import { NodeList } from "../components/NodeList";
import { OverviewNetwork } from "../components/OverviewNetwork";
import { TrafficChart, type TrafficSample } from "../components/TrafficChart";
import "./Overview.css";
import { liveUptime, operationPending } from "../lib/service";
import type { Mode, Traffic } from "../lib/types";

/* Real counter samples every 3 seconds, displayed over the last minute. */
const POLL_MS = 3000;

const MODES = [
  { id: "smart", label: tr("智能分流"), desc: tr("国内直连，其余按规则走代理") },
  { id: "global", label: tr("全局代理"), desc: tr("局域网全部流量经过代理") },
  { id: "dns", label: tr("仅 DNS 保护"), desc: tr("只加密 DNS 查询，防劫持和污染；其他流量直连") },
] as const;
const RUNTIME_MODES = [
  { id: "redirect_tproxy", label: tr("TProxy（推荐）") },
  { id: "redirect", label: tr("Redirect（仅 TCP）") },
  { id: "redirect_tun", label: "Redirect + TUN" },
  { id: "tun", label: "TUN" },
];

export function Overview() {
  const runtimeMode = useSignal("redirect_tproxy");
  const runtimeLoaded = useSignal(false);
  useAutosave(configWrites);
  const clock = useSignal(performance.now());
  usePoll(() => { clock.value = performance.now(); }, 1000, app.liveStatus && !!app.status?.running);
  const message = useSignal("");
  const totals = useSignal<{ up: number; down: number } | null>(null);
  const samples = useSignal<TrafficSample[]>([]);
  useEffect(() => {
    let disposed = false;
    const revision = configWrites.revision;
    const pending = configWrites.value;
    const apply = (result: { config: Record<string, string | null> }) => {
      if (disposed) return;
      if (revision === configWrites.revision) runtimeMode.value = configWrites.value?.["config.proxy_mode"] ?? pending?.["config.proxy_mode"] ?? result.config["config.proxy_mode"] ?? "redirect_tproxy";
      runtimeLoaded.value = true;
    };
    void call<{ config: Record<string, string | null> }>("get_config", {}, { onUpdate: apply }).then(apply).catch((error: Error) => { if (!disposed) message.value = error.message; });
    return () => { disposed = true; };
  }, []);

  /*
   * Poll clash_api totals every 3 s (only while the page is visible) and turn
   * the deltas into a rate + sparkline. per_source: false skips the per-device
   * sums the overview does not show.
   */
  const last = useRef<{ up: number; down: number; t: number } | null>(null);
  usePoll(async (resumed) => {
    if (resumed) {
      last.current = null;
      samples.value = [];
    }
    if (!app.liveStatus || !app.status?.running) {
      totals.value = null;
      samples.value = [];
      last.current = null;
      return;
    }
    try {
      const t = await call<Traffic>("traffic", { per_source: false });
      if (!t.running || t.download_total == null || t.upload_total == null) return;
      const now = Date.now();
      const prev = last.current;
      if (prev && t.upload_total >= prev.up && t.download_total >= prev.down && now > prev.t) {
        const dt = (now - prev.t) / 1000;
        const r = { up: Math.max(0, (t.upload_total - prev.up) / dt), down: Math.max(0, (t.download_total - prev.down) / dt) };
        samples.value = [...samples.value.filter((point) => now - point.time <= 60000), { time: now, ...r }].slice(-32);
      } else {
        samples.value = [];
      }
      last.current = { up: t.upload_total, down: t.download_total, t: now };
      totals.value = { up: t.upload_total, down: t.download_total };
    } catch {
      /* keep the last value */
    }
  }, POLL_MS);

  const toggle = () => {
    if (!app.liveStatus || !app.status) return;
    requestEnabled(!desiredEnabled(app.status));
  };

  const setMode = (mode: Mode) => {
    if (!app.liveStatus || !app.status) return;
    requestMode(mode);
  };

  const s = app.status;
  if (!s) return <main class="page muted">{tr("加载中…")}</main>;

  const pending = app.controlAction || (operationPending(s.operation) ? s.operation!.action : null);
  const live = app.liveStatus;
  const failed = live && s.enabled && !s.running && !pending;
  const requestedOn = desiredEnabled(s);
  const actionLabel = !live ? tr("正在连接路由器…") : pending === "stop" ? tr("正在停止…") : pending === "restart" ? tr("正在重启…") : pending ? tr("正在启动…") : s.running ? tr("停止") : failed ? tr("重试") : tr("启动");

  return (
    <main class="page overview-page">
      <Announcements />
      {live && s.health?.state === "failopen" && (
        <div class="banner warn">{tr("代理服务没有响应，Hyper 已暂时让局域网直连，避免整个网络断开。恢复后会自动切回代理")} <a href="#/diagnostics" style={{ marginLeft: "8px", color: "#fff" }}>{tr("查看日志 ›")}</a>
        </div>
      )}

      <div class="overview-dashboard">
        <div class="overview-control-column">
        <Card className="connection-card">
          <div class="connection-heading">
            <h2>{!live ? tr("正在连接路由器…") : pending ? actionLabel : s.running ? tr("已连接") : failed ? tr("连接没有成功") : tr("Hyper 已停止")}</h2>
            <p class="muted">
              {!live ? tr("正在读取路由器状态") : pending ? tr("正在应用连接设置，请稍候") : s.running
                ? tr("接入这台路由器的设备，正在经由 Hyper 访问网络")
                : failed
                  ? tr("可以换一个节点，或查看日志找原因")
                  : tr("启动后，接入这台路由器的设备会经由 Hyper 访问网络")}
            </p>
          </div>
          {live && (failed || (s.operation?.state === "failed" && !pending)) && (
            <div class="banner">{tr("服务操作未完成，请查看日志")} <a href="#/diagnostics" style={{ color: "#fff" }}>{tr("查看日志 ›")}</a>
            </div>
          )}
          <div class="connection-controls">
            <p class="connection-uptime" aria-label={live && s.running ? tr("运行时长") : undefined} aria-hidden={!live || !s.running}>{live && s.running ? formatUptime(liveUptime(s.uptime, app.statusAt, clock.value)) : "\u00a0"}</p>
            <div class="power-control">
            <button type="button" class={`power-button ${live && requestedOn ? "is-on" : ""} ${pending || !live ? "is-busy" : ""}`} aria-busy={!!pending || !live} disabled={!live || (!requestedOn && !s.main_node)} onClick={toggle}>
              <span class="power-symbol" aria-hidden="true">
                <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                  <path d="M12 3v9M6.2 5.8a8.5 8.5 0 1 0 11.6 0" />
                </svg>
              </span>
              <span>{!live ? tr("加载中") : requestedOn ? tr("停止") : failed && !pending ? tr("重试") : tr("启动")}</span>
            </button>
            {live && !s.main_node && <span class="muted">{tr("还没有节点，请先在下方更新节点")}</span>}
          </div>
          </div>
          {(app.controlError || app.modeError || message.value) && <div class="banner">{serviceText(app.controlError || app.modeError || message.value)}</div>}
          <div class="connection-mode-section">
          <h3 style={{ margin: 0, fontSize: "15px" }}>{tr("代理模式")}</h3>
          <div role="group" aria-label={tr("代理模式")} aria-busy={!!app.modeDesired} class="connection-modes">
            {MODES.map((m) => (
              <button key={m.id} type="button" class="option" aria-pressed={(app.modeDesired || s.mode) === m.id} aria-busy={app.modeDesired === m.id} aria-label={app.modeDesired === m.id ? `${m.label} · ${tr("正在切换…")}` : m.label} disabled={!live} title={m.desc} onClick={() => setMode(m.id)}>
                <span class="grow">
                  <span aria-live="polite">{app.modeDesired === m.id ? tr("正在切换…") : m.label}</span>
                </span>
              </button>
            ))}
          </div>
          <div class="connection-runtime-mode"><Row title={tr("运行模式")}>
            <select class="input" aria-label={tr("运行模式")} value={configWrites.value?.["config.proxy_mode"] ?? runtimeMode.value} disabled={!live || !runtimeLoaded.value} onChange={(event) => {
              runtimeMode.value = event.currentTarget.value;
              configWrites.enqueue({ "config.proxy_mode": event.currentTarget.value });
            }}>
              {RUNTIME_MODES.map(mode => <option key={mode.id} value={mode.id}>{mode.label}</option>)}
            </select>
          </Row></div>
          </div>
      <div class="overview-telemetry">
        <div class="traffic-summary">
          <h3>{tr("实时流量")}</h3>
          <span class="muted small">{tr("累计 ↓")} {formatBytes(live && s.running ? totals.value?.down : undefined)} · ↑ {formatBytes(live && s.running ? totals.value?.up : undefined)}</span>
        </div>
        <TrafficChart samples={live && s.running ? samples.value : []} />
      </div>
        </Card>
        <OverviewNetwork section="custom" />
        <OverviewNetwork section="rules" />
      </div>

      <div class="overview-content-column">


      <NodeList />
      <OverviewNetwork section="dns" />
      <OverviewNetwork section="devices" />
      </div>
      </div>
    </main>
  );
}
