import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useRef } from "preact/hooks";
import { app } from "../lib/app";
import { usePoll } from "../lib/poll";
import { formatAgo, formatBytes, formatRate, formatUptime } from "../lib/format";
import { call } from "../lib/rpc";
import { serviceText } from "../lib/display";
import { nodeLabel } from "../lib/regions";
import { NodeList } from "../components/NodeList";
import { liveUptime, operationPending } from "../lib/service";
import type { Result, ServiceOperation, Traffic } from "../lib/types";

/* traffic sample every 3 s, 20 bars = the last minute */
const POLL_MS = 3000;
const BARS = 20;

const MODES = [
  { id: "smart", label: tr("智能分流"), desc: tr("国内直连，其余按规则走代理") },
  { id: "global", label: tr("全局代理"), desc: tr("局域网全部流量经过代理") },
  { id: "dns", label: tr("仅 DNS 保护"), desc: tr("只加密 DNS 查询，防劫持和污染；其他流量直连") },
] as const;

export function Overview() {
  const busy = useSignal(false);
  const clock = useSignal(performance.now());
  usePoll(() => { clock.value = performance.now(); }, 1000, !!app.status?.running);
  const message = useSignal("");
  const rate = useSignal<{ up: number; down: number } | null>(null);
  const totals = useSignal<{ up: number; down: number } | null>(null);
  const bars = useSignal<number[]>([]);

  /*
   * Poll clash_api totals every 3 s (only while the page is visible) and turn
   * the deltas into a rate + sparkline. per_source: false skips the per-device
   * sums the overview does not show.
   */
  const last = useRef<{ up: number; down: number; t: number } | null>(null);
  usePoll(async (resumed) => {
    if (resumed) last.current = null; /* a gap of minutes is not a rate */
    if (!app.status?.running) {
      rate.value = null;
      return;
    }
    try {
      const t = await call<Traffic>("traffic", { per_source: false });
      if (!t.running || t.download_total == null || t.upload_total == null) return;
      const now = Date.now();
      const prev = last.current;
      if (prev) {
        const dt = (now - prev.t) / 1000;
        const r = { up: Math.max(0, (t.upload_total - prev.up) / dt), down: Math.max(0, (t.download_total - prev.down) / dt) };
        rate.value = r;
        bars.value = [...bars.value, r.down].slice(-BARS);
      }
      last.current = { up: t.upload_total, down: t.download_total, t: now };
      totals.value = { up: t.upload_total, down: t.download_total };
    } catch {
      /* keep the last value */
    }
  }, POLL_MS);

  const toggle = async () => {
    if (!app.status || busy.value || operationPending(app.status.operation)) return;
    busy.value = true;
    const enabled = !app.status.running;
    app.controlAction = enabled ? "start" : "stop";
    message.value = "";
    try {
      const r = await call<Result & { operation?: ServiceOperation }>("set_enabled", { enabled });
      if (!r.result) message.value = tr("操作没有成功，请查看日志");
      else if (r.operation) app.status = { ...app.status, operation: r.operation };
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = false;
    app.controlAction = null;
    app.tick++;
  };

  const setMode = async (mode: string) => {
    busy.value = true;
    try {
      await call("set_mode", { mode });
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = false;
    app.tick++;
  };

  const s = app.status;
  if (!s) return <main class="page muted">{tr("加载中…")}</main>;

  const pending = app.controlAction || (operationPending(s.operation) ? s.operation!.action : null);
  const failed = s.enabled && !s.running && !pending;
  const isBusy = busy.value || !!pending;
  const actionLabel = pending === "stop" ? tr("正在停止…") : pending === "restart" ? tr("正在重启…") : pending ? tr("正在启动…") : s.running ? tr("停止") : failed ? tr("重试") : tr("启动");
  const peak = Math.max(1, ...bars.value);

  return (
    <main class="page">
      {s.health?.state === "failopen" && (
        <div class="banner warn">{tr("代理服务没有响应，Hyper 已暂时让局域网直连，避免整个网络断开。恢复后会自动切回代理")} <a href="#/logs" style={{ marginLeft: "8px", color: "#fff" }}>{tr("查看日志 ›")}</a>
        </div>
      )}

      <div class="row">
        <section class="card" style={{ flex: "3 1 460px" }}>
          <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
            <h2 style={{ fontSize: "24px" }}>{pending ? actionLabel : s.running ? tr("世界很大，自在连接") : failed ? tr("连接没有成功") : tr("Hyper 已停止")}</h2>
            <p class="muted" style={{ margin: 0 }}>
              {pending ? tr("正在应用连接设置，请稍候") : s.running
                ? tr("接入这台路由器的设备，正在经由 Hyper 访问网络")
                : failed
                  ? tr("可以换一个节点，或查看日志找原因")
                  : tr("启动后，接入这台路由器的设备会经由 Hyper 访问网络")}
            </p>
          </div>
          {(failed || (s.operation?.state === "failed" && !pending)) && (
            <div class="banner">{tr("服务操作未完成，请查看日志")} <a href="#/logs" style={{ color: "#fff" }}>{tr("查看日志 ›")}</a>
            </div>
          )}
          <dl class="grid-stats" style={{ margin: 0 }}>
            <div class="stat"><b>{s.running ? formatUptime(liveUptime(s.uptime, app.statusAt, clock.value)) : "--"}</b><span>{tr("运行时长")}</span></div>
            <div class="stat"><b>{s.memory_kb ? `${Math.round(s.memory_kb / 1024)} MB` : "--"}</b><span>{tr("内存占用")}</span></div>
            <div class="stat"><b>{s.version || "--"}</b><span>{tr("软件版本")}</span></div>
            <div class="stat"><b>{s.sync?.count ?? "--"}</b><span>{tr("节点 · {0}更新", [formatAgo(s.sync?.time)])}</span></div>
          </dl>
          <div class="power-control">
            <button type="button" class={`power-button ${s.running ? "is-on" : ""} ${pending ? "is-busy" : ""}`} aria-busy={!!pending} disabled={isBusy || (!s.running && !s.main_node)} onClick={() => toggle()}>
              <span class="power-symbol" aria-hidden="true">
                <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                  <path d="M12 3v9M6.2 5.8a8.5 8.5 0 1 0 11.6 0" />
                </svg>
              </span>
              <span>{actionLabel}</span>
            </button>
            <span class="muted small" role="status">{pending ? tr("正在应用连接设置，请稍候") : s.running ? tr("已连接，局域网设备可通过 Hyper 上网") : tr("一键连接，让局域网设备自在上网")}</span>
            {!s.main_node && <span class="muted">{tr("还没有节点，请先在下方更新节点")}</span>}
          </div>
          {message.value && <div class="banner">{serviceText(message.value)}</div>}
        </section>

        <section class="card" style={{ flex: "2 1 320px" }}>
          <div class="card-head">
            <h2>{tr("当前节点")}</h2>
            <button type="button" class="btn ghost" onClick={() => document.getElementById("overview-nodes")?.scrollIntoView()}>{tr("切换 ›")}</button>
          </div>
          <div style={{ fontSize: "17px", fontWeight: 600 }}>{nodeLabel(s.main_node_label || tr("未选择"))}</div>
          <div style={{ height: "1px", background: "var(--line)" }} />
          <h3 style={{ margin: 0, fontSize: "15px" }}>{tr("代理模式")}</h3>
          <div role="group" aria-label={tr("代理模式")} style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
            {MODES.map((m) => (
              <button key={m.id} type="button" class="option" aria-pressed={s.mode === m.id} disabled={isBusy} onClick={() => setMode(m.id)}>
                <span class="grow">
                  <span style={{ fontSize: "15px" }}>{m.label}</span>
                  <span class="muted small">{m.desc}</span>
                </span>
              </button>
            ))}
          </div>
        </section>
      </div>

      <NodeList />

      <div class="row">
        <section class="card" style={{ flex: "3 1 380px" }}>
          <div class="card-head">
            <h2>{tr("实时流量")}</h2>
            <span class="muted small">{tr("累计 ↓")} {formatBytes(totals.value?.down)} · ↑ {formatBytes(totals.value?.up)}</span>
          </div>
          <div style={{ display: "flex", gap: "28px", flexWrap: "wrap" }}>
            <div class="stat"><b style={{ fontSize: "22px" }}>{formatRate(rate.value?.down)}</b><span>{tr("下载")}</span></div>
            <div class="stat"><b style={{ fontSize: "22px" }}>{formatRate(rate.value?.up)}</b><span>{tr("上传")}</span></div>
          </div>
          <div aria-hidden="true" style={{ display: "flex", alignItems: "flex-end", gap: "4px", height: "72px" }}>
            {Array.from({ length: BARS }, (_, i) => {
              const v = bars.value[i - (BARS - bars.value.length)];
              const h = v == null ? 4 : Math.max(4, Math.round((v / peak) * 72));
              return <span key={i} style={{ flex: "1 1 0", minWidth: "3px", borderRadius: "3px", height: `${h}px`, background: v == null ? "#3a3a3d" : "var(--accent)" }} />;
            })}
          </div>
          <span class="muted small">{tr("最近 60 秒的实时流量")}</span>
        </section>
      </div>
    </main>
  );
}
