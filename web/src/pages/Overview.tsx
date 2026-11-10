import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { app } from "../lib/app";
import { formatAgo, formatBytes, formatRate, formatUptime } from "../lib/format";
import { call } from "../lib/rpc";
import type { Result, Traffic } from "../lib/types";

const MODES = [
  { id: "smart", label: "智能分流", desc: "国内直连，其余按规则走代理" },
  { id: "global", label: "全局代理", desc: "局域网全部流量经过代理" },
  { id: "dns", label: "仅 DNS 保护", desc: "只加密 DNS 查询，防劫持和污染；其他流量直连" },
] as const;

export function Overview() {
  const busy = useSignal(false);
  const message = useSignal("");
  const rate = useSignal<{ up: number; down: number } | null>(null);
  const totals = useSignal<{ up: number; down: number } | null>(null);
  const bars = useSignal<number[]>([]);

  /* Poll clash_api totals every 2 s and turn the deltas into a rate + sparkline */
  useEffect(() => {
    let last: { up: number; down: number; t: number } | null = null;
    const poll = async () => {
      if (!app.status?.running) {
        rate.value = null;
        return;
      }
      try {
        const t = await call<Traffic>("traffic");
        if (!t.running || t.download_total == null || t.upload_total == null) return;
        const now = Date.now();
        if (last) {
          const dt = (now - last.t) / 1000;
          const r = { up: Math.max(0, (t.upload_total - last.up) / dt), down: Math.max(0, (t.download_total - last.down) / dt) };
          rate.value = r;
          bars.value = [...bars.value, r.down].slice(-30);
        }
        last = { up: t.upload_total, down: t.download_total, t: now };
        totals.value = { up: t.upload_total, down: t.download_total };
      } catch {
        /* keep the last value */
      }
    };
    poll();
    const id = setInterval(poll, 2000);
    return () => clearInterval(id);
  }, []);

  const toggle = async () => {
    if (!app.status) return;
    busy.value = true;
    message.value = "";
    try {
      const r = await call<Result & { running: boolean }>("set_enabled", { enabled: !app.status.enabled });
      if (!r.result) message.value = "操作没有成功，请查看日志。";
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = false;
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
  if (!s) return <main class="page muted">加载中…</main>;

  const failed = s.enabled && !s.running;
  const isBusy = busy.value;
  const peak = Math.max(1, ...bars.value);

  return (
    <main class="page">
      {s.health?.state === "failopen" && (
        <div class="banner warn">
          sing-box 没有响应，Hyper 已暂时让局域网直连，避免整个网络断开。恢复后会自动切回代理。
          <a href="#/logs" style={{ marginLeft: "8px", color: "#fff" }}>查看日志 ›</a>
        </div>
      )}

      <div class="row">
        <section class="card" style={{ flex: "3 1 460px" }}>
          <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
            <h2 style={{ fontSize: "24px" }}>{s.running ? "世界很大，自在连接。" : failed ? "连接没有成功" : "Hyper 已停止"}</h2>
            <p class="muted" style={{ margin: 0 }}>
              {s.running
                ? "接入这台路由器的设备，正在经由 Hyper 访问网络。"
                : failed
                  ? "可以换一个节点，或查看日志找原因。"
                  : "启动后，接入这台路由器的设备会经由 Hyper 访问网络。"}
            </p>
          </div>
          {failed && (
            <div class="banner">
              sing-box 没有运行。<a href="#/logs" style={{ color: "#fff" }}>查看日志 ›</a>
            </div>
          )}
          <dl class="grid-stats" style={{ margin: 0 }}>
            <div class="stat"><b>{s.running ? formatUptime(s.uptime) : "--"}</b><span>运行时长</span></div>
            <div class="stat"><b>{s.memory_kb ? `${Math.round(s.memory_kb / 1024)} MB` : "--"}</b><span>内存占用</span></div>
            <div class="stat"><b>{s.singbox_version ? `sing-box ${s.singbox_version}` : "--"}</b><span>核心</span></div>
            <div class="stat"><b>{s.sync?.count ?? "--"}</b><span>节点 · {formatAgo(s.sync?.time)}同步</span></div>
          </dl>
          <div style={{ display: "flex", flexWrap: "wrap", gap: "12px" }}>
            <button type="button" class={`btn big ${s.enabled ? "danger" : "primary"}`} disabled={isBusy || !s.main_node} onClick={() => toggle()}>
              {s.enabled ? "停止" : failed ? "重试" : "启动"}
            </button>
            {!s.main_node && <span class="muted" style={{ alignSelf: "center" }}>还没有节点，先在“节点”页同步。</span>}
          </div>
          {message.value && <div class="banner">{message.value}</div>}
        </section>

        <section class="card" style={{ flex: "2 1 320px" }}>
          <div class="card-head">
            <h2>当前节点</h2>
            <a href="#/nodes" style={{ minHeight: "44px", display: "inline-flex", alignItems: "center" }}>切换 ›</a>
          </div>
          <div style={{ fontSize: "17px", fontWeight: 600 }}>{s.main_node_label || "未选择"}</div>
          <div style={{ height: "1px", background: "var(--line)" }} />
          <h3 style={{ margin: 0, fontSize: "15px" }}>代理模式</h3>
          <div role="group" aria-label="代理模式" style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
            {MODES.map((m) => (
              <button key={m.id} type="button" class="option" aria-pressed={s.mode === m.id} disabled={busy.value} onClick={() => setMode(m.id)}>
                <span class="grow">
                  <span style={{ fontSize: "15px" }}>{m.label}</span>
                  <span class="muted small">{m.desc}</span>
                </span>
              </button>
            ))}
          </div>
        </section>
      </div>

      <div class="row">
        <section class="card" style={{ flex: "3 1 380px" }}>
          <div class="card-head">
            <h2>实时流量</h2>
            <span class="muted small">累计 ↓ {formatBytes(totals.value?.down)} · ↑ {formatBytes(totals.value?.up)}</span>
          </div>
          <div style={{ display: "flex", gap: "28px", flexWrap: "wrap" }}>
            <div class="stat"><b style={{ fontSize: "22px" }}>{formatRate(rate.value?.down)}</b><span>下载</span></div>
            <div class="stat"><b style={{ fontSize: "22px" }}>{formatRate(rate.value?.up)}</b><span>上传</span></div>
          </div>
          <div aria-hidden="true" style={{ display: "flex", alignItems: "flex-end", gap: "4px", height: "72px" }}>
            {Array.from({ length: 30 }, (_, i) => {
              const v = bars.value[i - (30 - bars.value.length)];
              const h = v == null ? 4 : Math.max(4, Math.round((v / peak) * 72));
              return <span key={i} style={{ flex: "1 1 0", minWidth: "3px", borderRadius: "3px", height: `${h}px`, background: v == null ? "#3a3a3d" : "var(--accent)" }} />;
            })}
          </div>
          <span class="muted small">最近 60 秒，数据来自 sing-box clash_api</span>
        </section>
      </div>
    </main>
  );
}
