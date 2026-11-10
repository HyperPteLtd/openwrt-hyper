import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { useStore } from "../lib/store";
import { Logo } from "../components/TopBar";
import { Notice, Row, Switch } from "../components/ui";
import { app } from "../lib/app";
import { formatAgo } from "../lib/format";
import { call } from "../lib/rpc";
import type { Result } from "../lib/types";

type Config = Record<string, string | null>;

interface UpdateInfo extends Result {
  current?: string | null;
  latest?: string;
  published_at?: string;
  notes?: string;
  url?: string;
  installable?: boolean;
  package_manager?: string;
  update_state?: { state: "installing" | "done" | "failed"; version: string; time: number } | null;
}

const PROXY_MODES = [
  { id: "redirect_tproxy", label: "TProxy（推荐，支持 UDP）" },
  { id: "redirect", label: "Redirect（仅 TCP）" },
  { id: "redirect_tun", label: "Redirect + TUN" },
  { id: "tun", label: "TUN" },
];
const LOG_LEVELS = ["error", "warn", "info", "debug"];

export function Settings() {
  const saved = useSignal<Config>({});
  const draft = useStore<{ v: Config }>({ v: {} });
  const apiBase = useSignal("");
  const msg = useStore({ text: "", tone: "error" as "error" | "warn" | "ok" });
  const busy = useSignal("");
  const update = useSignal<UpdateInfo | null>(null);

  useEffect(() => {
    call<{ config: Config; api_base: string }>("get_config")
      .then((r) => {
        saved.value = r.config;
        draft.v = { ...r.config };
        apiBase.value = r.api_base || "";
      })
      .catch((e: Error) => (msg.text = e.message));
  }, []);

  const set = (key: string, value: string) => {
    draft.v = { ...draft.v, [key]: value };
  };
  const flag = (key: string) => draft.v[key] === "1";
  const changed = Object.keys(draft.v).filter((k) => (draft.v[k] ?? "") !== (saved.value[k] ?? ""));
  const isBusy = !!busy.value;

  const save = async () => {
    const values: Record<string, string> = {};
    for (const k of Object.keys(draft.v)) if ((draft.v[k] ?? "") !== (saved.value[k] ?? "")) values[k] = draft.v[k] ?? "";
    busy.value = "save";
    try {
      const r = await call<Result>("set_config", { values });
      if (r.result) {
        saved.value = { ...draft.v };
        msg.text = "已保存。服务在运行时会自动重载。";
        msg.tone = "ok";
      } else {
        msg.text = r.error || "保存失败";
        msg.tone = "error";
      }
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
    busy.value = "";
    app.tick++;
  };

  const saveApiBase = async () => {
    busy.value = "api";
    try {
      const r = await call<Result>("set_api_base", { api_base: apiBase.value.trim() });
      msg.text = r.result ? "服务地址已保存。地址变了的话需要重新登录。" : r.error === "illegal url" ? "地址格式不对，应以 https:// 开头。" : r.error || "保存失败";
      msg.tone = r.result ? "ok" : "error";
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
    busy.value = "";
    app.tick++;
  };

  const checkUpdate = async () => {
    busy.value = "check";
    try {
      update.value = await call<UpdateInfo>("check_update");
    } catch (e) {
      update.value = { result: false, error: (e as Error).message };
    }
    busy.value = "";
  };

  const installUpdate = async () => {
    busy.value = "install";
    try {
      const r = await call<Result & { started?: boolean }>("install_update");
      if (!r.result) {
        msg.text = r.error === "up_to_date" ? "已经是最新版本。" : `更新失败：${r.error}`;
        msg.tone = r.error === "up_to_date" ? "ok" : "error";
        busy.value = "";
        return;
      }
      /* the package replaces the backend; poll until it reports done/failed */
      for (let i = 0; i < 60; i++) {
        await new Promise((res) => setTimeout(res, 3000));
        try {
          const u = await call<UpdateInfo>("check_update");
          update.value = u;
          if (u.update_state && u.update_state.state !== "installing") break;
        } catch {
          /* rpcd reloads during the install */
        }
      }
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
    busy.value = "";
  };

  const s = app.status;
  const u = update.value;
  const hasNew = !!(u?.result && u.latest && u.latest !== u.current);

  return (
    <main class="page">
      <Notice text={msg.text} tone={msg.tone} />
      <div class="row">
        <div style={{ flex: "1 1 460px", display: "flex", flexDirection: "column", gap: "20px", minWidth: 0 }}>
          <section class="card">
            <h2>服务器与订阅</h2>
            <label class="field">
              服务地址（PPanel API）
              <span style={{ display: "flex", gap: "10px" }}>
                <input class="input mono" type="url" value={apiBase.value} onInput={(e) => (apiBase.value = e.currentTarget.value)} placeholder="https://api.example.com" />
                <button type="button" class="btn" style={{ borderRadius: "12px", minHeight: "48px", flexShrink: 0 }} disabled={busy.value === "api"} onClick={() => saveApiBase()}>
                  保存
                </button>
              </span>
              <span class="muted small">修改后需要重新登录。</span>
            </label>
            <div>
              <Row title="自动同步节点" desc={`每天在设定的时间同步一次 · 上次 ${formatAgo(s?.sync?.time)}`}>
                <Switch label="自动同步节点" checked={flag("subscription.auto_update")} onToggle={(on) => set("subscription.auto_update", on ? "1" : "0")} />
              </Row>
              <Row title="同步时间" desc="路由器本地时间">
                <select
                  aria-label="同步时间"
                  class="input"
                  style={{ width: "auto", minHeight: "40px" }}
                  onChange={(e) => set("subscription.auto_update_time", e.currentTarget.value)}
                >
                  {Array.from({ length: 24 }, (_, h) => (
                    <option key={h} value={String(h)} selected={String(h) === (draft.v["subscription.auto_update_time"] || "4")}>{`${String(h).padStart(2, "0")}:00`}</option>
                  ))}
                </select>
              </Row>
              <Row title="经代理同步" desc="服务地址在本地网络直连不通时打开">
                <Switch label="经代理同步" checked={flag("subscription.update_via_proxy")} onToggle={(on) => set("subscription.update_via_proxy", on ? "1" : "0")} />
              </Row>
            </div>
          </section>

          <section class="card">
            <h2>路由器</h2>
            <div>
              <Row title="透明代理方式" desc="TProxy 同时代理 TCP 和 UDP">
                <select aria-label="透明代理方式" class="input" style={{ width: "auto", minHeight: "40px" }} onChange={(e) => set("config.proxy_mode", e.currentTarget.value)}>
                  {PROXY_MODES.map((m) => (
                    <option key={m.id} value={m.id} selected={m.id === (draft.v["config.proxy_mode"] || "redirect_tproxy")}>{m.label}</option>
                  ))}
                </select>
              </Row>
              <Row title="IPv6" desc="同时代理 IPv6 流量">
                <Switch label="IPv6" checked={flag("config.ipv6_support")} onToggle={(on) => set("config.ipv6_support", on ? "1" : "0")} />
              </Row>
              <Row title="路由器自身流量走代理" desc="比如路由器上的软件更新、节点同步">
                <Switch label="路由器自身流量走代理" checked={draft.v["control.proxy_router_self"] !== "0"} onToggle={(on) => set("control.proxy_router_self", on ? "1" : "0")} />
              </Row>
              <Row title="日志级别">
                <select aria-label="日志级别" class="input" style={{ width: "auto", minHeight: "40px" }} onChange={(e) => set("config.log_level", e.currentTarget.value)}>
                  {LOG_LEVELS.map((l) => (
                    <option key={l} value={l} selected={l === (draft.v["config.log_level"] || "warn")}>{l}</option>
                  ))}
                </select>
              </Row>
            </div>
          </section>
        </div>

        <div style={{ flex: "1 1 360px", display: "flex", flexDirection: "column", gap: "20px", minWidth: 0 }}>
          <section class="card">
            <div style={{ display: "flex", alignItems: "center", gap: "14px" }}>
              <Logo size={40} />
              <div style={{ display: "flex", flexDirection: "column", gap: "4px" }}>
                <h2>关于 Hyper 路由器版</h2>
                <span class="muted small mono">
                  luci-app-hyper {s?.version || "?"} · sing-box {s?.singbox_version || "?"}
                </span>
              </div>
            </div>
            <div style={{ borderRadius: "14px", background: "var(--card-2)", padding: "14px 16px", display: "flex", flexDirection: "column", gap: "10px" }}>
              <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "8px 12px" }}>
                <span style={{ flex: "1 1 200px", display: "flex", flexDirection: "column", gap: "4px" }}>
                  <b style={{ color: hasNew || u?.update_state?.state === "done" ? "var(--accent)" : "var(--text)" }}>
                    {u?.update_state?.state === "installing"
                      ? `正在安装 ${u.update_state.version}…`
                      : u?.update_state?.state === "done"
                        ? `已更新到 ${u.update_state.version}，刷新页面后生效`
                        : u?.update_state?.state === "failed"
                          ? "安装失败，请查看日志"
                          : !u
                            ? `当前 ${s?.version || "?"}`
                            : !u.result
                              ? "检查更新失败"
                              : hasNew
                                ? `发现新版本 ${u.latest}`
                                : "已是最新版本"}
                  </b>
                  <span class="muted small">
                    {u && !u.result ? u.error : u?.published_at ? `发布于 ${u.published_at.slice(0, 10)} · GitHub Releases` : "来源：GitHub Releases"}
                  </span>
                </span>
                {hasNew && u?.installable ? (
                  <button type="button" class="btn primary" disabled={!!busy.value} onClick={() => installUpdate()}>
                    {busy.value === "install" ? "安装中…" : "下载并安装"}
                  </button>
                ) : (
                  <button type="button" class="btn" disabled={!!busy.value} onClick={() => checkUpdate()}>
                    {busy.value === "check" ? "检查中…" : "检查更新"}
                  </button>
                )}
              </div>
              {hasNew && u?.notes && <pre class="mono small" style={{ margin: 0, whiteSpace: "pre-wrap", color: "#c8c8cc", maxHeight: "160px", overflow: "auto" }}>{u.notes}</pre>}
              <Row title="每天自动检查更新" desc="只提醒，不会自动安装">
                <Switch label="每天自动检查更新" checked={flag("update.auto_check")} onToggle={(on) => set("update.auto_check", on ? "1" : "0")} />
              </Row>
            </div>
            <div style={{ display: "flex", flexWrap: "wrap", gap: "4px 18px" }}>
              <a href="https://github.com/HyperPteLtd/openwrt-hyper" target="_blank" rel="noreferrer">GitHub</a>
              <a href="https://github.com/HyperPteLtd/openwrt-hyper/releases" target="_blank" rel="noreferrer">版本记录</a>
            </div>
          </section>
        </div>
      </div>

      <div style={{ position: "sticky", bottom: 0, display: "flex", flexWrap: "wrap", justifyContent: "flex-end", alignItems: "center", gap: "10px", padding: "14px 0", background: "var(--bg)", borderTop: "1px solid var(--control)" }}>
        <span class="muted" style={{ flexGrow: 1 }}>{changed.length ? `有 ${changed.length} 项未保存的修改` : ""}</span>
        <button type="button" class="btn" disabled={!changed.length || isBusy} onClick={() => (draft.v = { ...saved.value })}>重置</button>
        <button type="button" class="btn primary" disabled={!changed.length || isBusy} onClick={() => save()}>{busy.value === "save" ? "保存中…" : "保存并应用"}</button>
      </div>
    </main>
  );
}
