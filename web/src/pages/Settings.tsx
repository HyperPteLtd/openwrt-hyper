import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { useStore } from "../lib/store";
import { Logo } from "../components/TopBar";
import { Notice, Row, Switch } from "../components/ui";
import { app } from "../lib/app";
import { formatAgo } from "../lib/format";
import { serviceText } from "../lib/display";
import { languagePreference, saveLanguage, type LanguagePreference } from "../lib/i18n";
import { call } from "../lib/rpc";
import type { Result } from "../lib/types";

type Config = Record<string, string | null>;

interface UpdateInfo extends Result {
  current?: string | null;
  latest?: string;
  /** compared as versions by the backend: installed "0.2.0-r1" is release "v0.2.0" */
  update_available?: boolean;
  published_at?: string;
  notes?: string;
  url?: string;
  installable?: boolean;
  package_manager?: string;
  update_state?: { state: "installing" | "done" | "failed"; version: string; time: number } | null;
}

const PROXY_MODES = [
  { id: "redirect_tproxy", label: tr("TProxy（推荐）") },
  { id: "redirect", label: tr("Redirect（仅 TCP）") },
  { id: "redirect_tun", label: "Redirect + TUN" },
  { id: "tun", label: "TUN" },
];
const LOG_LEVELS = ["error", "warn", "info", "debug"];

export function Settings() {
  const language = useSignal<LanguagePreference>(languagePreference);
  const saved = useSignal<Config>({});
  const draft = useStore<{ v: Config }>({ v: {} });
  const msg = useStore({ text: "", tone: "error" as "error" | "warn" | "ok" });
  const busy = useSignal("");
  const update = useSignal<UpdateInfo | null>(null);

  useEffect(() => {
    call<{ config: Config }>("get_config")
      .then((r) => {
        saved.value = r.config;
        draft.v = { ...r.config };
      })
      .catch((e: Error) => (msg.text = e.message));
  }, []);

  const set = (key: string, value: string) => {
    draft.v = { ...draft.v, [key]: value };
  };
  const flag = (key: string) => draft.v[key] === "1";
  const changed = Object.keys(draft.v).filter((k) => (draft.v[k] ?? "") !== (saved.value[k] ?? ""));
  if (language.value !== languagePreference) changed.push("language");
  const isBusy = !!busy.value;

  const save = async () => {
    const values: Record<string, string> = {};
    for (const k of Object.keys(draft.v)) if ((draft.v[k] ?? "") !== (saved.value[k] ?? "")) values[k] = draft.v[k] ?? "";
    busy.value = "save";
    try {
      const r = Object.keys(values).length ? await call<Result>("set_config", { values }) : { result: true };
      if (r.result) {
        saved.value = { ...draft.v };
        msg.text = tr("已保存。服务在运行时会自动重载");
        msg.tone = "ok";
        if (language.value !== languagePreference) {
          saveLanguage(language.value);
          location.reload();
        }
      } else {
        msg.text = r.error || tr("保存失败");
        msg.tone = "error";
      }
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
    busy.value = "";
    app.tick++;
  };

  const checkUpdate = async (fresh = true) => {
    busy.value = "check";
    try {
      update.value = await call<UpdateInfo>("check_update", {}, { fresh });
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
        msg.text = r.error === "up_to_date" ? tr("已经是最新版本") : tr("更新失败：{0}", [r.error]);
        msg.tone = r.error === "up_to_date" ? "ok" : "error";
        busy.value = "";
        return;
      }
      /* the package replaces the backend; poll until it reports done/failed */
      for (let i = 0; i < 60; i++) {
        await new Promise((res) => setTimeout(res, 3000));
        try {
          const u = await call<UpdateInfo>("check_update", {}, { fresh: true });
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

  /* the daily check found a new version: load its notes and package right away */
  const pending = !!app.status?.update?.available;
  useEffect(() => {
    if (pending && !update.value) checkUpdate(false);
  }, [pending]);

  const s = app.status;
  const u = update.value;
  const hasNew = !!(u?.result && u.update_available);

  return (
    <main class="page">
      <Notice text={msg.text} tone={msg.tone} />
      <div class="row">
        <div style={{ flex: "1 1 460px", display: "flex", flexDirection: "column", gap: "20px", minWidth: 0 }}>
          <section class="card">
            <h2>{tr("服务与节点")}</h2>
            <div>
              <Row title={tr("界面语言")} desc={tr("默认跟随路由器的 LuCI 语言，保存后生效")}>
                <select class="input settings-select" aria-label={tr("界面语言")} value={language.value} disabled={isBusy} onChange={(e) => { language.value = e.currentTarget.value as LanguagePreference; }}>
                  <option value="auto">{tr("跟随路由器")}</option>
                  <option value="zh-CN">{tr("简体中文")}</option>
                  <option value="en-US">English</option>
                </select>
              </Row>
              <Row title={tr("服务连接")} desc={tr("自动获取服务地址，连接异常时切换备用地址")}>
                <span class="muted small">{tr("自动选择")}</span>
              </Row>
              <Row title={tr("自动同步节点")} desc={tr("每天在设定的时间同步一次 · 上次 {0}", [formatAgo(s?.sync?.time)])}>
                <Switch label={tr("自动同步节点")} checked={flag("subscription.auto_update")} onToggle={(on) => set("subscription.auto_update", on ? "1" : "0")} />
              </Row>
              <Row title={tr("同步时间")} desc={tr("路由器本地时间")}>
                <select
                  aria-label={tr("同步时间")}
                  class="input settings-select"
                  value={draft.v["subscription.auto_update_time"] ?? "4"}
                  disabled={isBusy}
                  onChange={(e) => set("subscription.auto_update_time", e.currentTarget.value)}
                >
                  {Array.from({ length: 24 }, (_, h) => (
                    <option key={h} value={String(h)}>{`${String(h).padStart(2, "0")}:00`}</option>
                  ))}
                </select>
              </Row>
              <Row title={tr("经代理同步")} desc={tr("服务地址在本地网络直连不通时打开")}>
                <Switch label={tr("经代理同步")} checked={flag("subscription.update_via_proxy")} onToggle={(on) => set("subscription.update_via_proxy", on ? "1" : "0")} />
              </Row>
            </div>
          </section>

          <section class="card">
            <h2>{tr("路由器")}</h2>
            <div>
              <Row title={tr("透明代理方式")} desc={tr("TProxy 同时代理 TCP 和 UDP")}>
                <select aria-label={tr("透明代理方式")} class="input settings-select" value={draft.v["config.proxy_mode"] || "redirect_tproxy"} disabled={isBusy} onChange={(e) => set("config.proxy_mode", e.currentTarget.value)}>
                  {PROXY_MODES.map((m) => (
                    <option key={m.id} value={m.id}>{m.label}</option>
                  ))}
                </select>
              </Row>
              <Row title="IPv6" desc={tr("同时代理 IPv6 流量")}>
                <Switch label="IPv6" checked={flag("config.ipv6_support")} onToggle={(on) => set("config.ipv6_support", on ? "1" : "0")} />
              </Row>
              <Row title={tr("路由器自身流量走代理")} desc={tr("比如路由器上的软件更新、节点同步")}>
                <Switch label={tr("路由器自身流量走代理")} checked={draft.v["control.proxy_router_self"] !== "0"} onToggle={(on) => set("control.proxy_router_self", on ? "1" : "0")} />
              </Row>
              <Row title={tr("日志级别")}>
                <select aria-label={tr("日志级别")} class="input settings-select" value={draft.v["config.log_level"] || "warn"} disabled={isBusy} onChange={(e) => set("config.log_level", e.currentTarget.value)}>
                  {LOG_LEVELS.map((l) => (
                    <option key={l} value={l}>{l}</option>
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
                <h2>{tr("关于 Hyper 路由器版")}</h2>
                <span class="muted small mono">{tr("版本")} {s?.version || "--"}
                </span>
              </div>
            </div>
            <div>
              <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "8px 12px", minHeight: "64px", padding: "8px 0", borderTop: "1px solid var(--line)" }}>
                <span style={{ flex: "1 1 200px", display: "flex", flexDirection: "column", gap: "4px" }}>
                  <b style={{ color: hasNew || u?.update_state?.state === "done" ? "var(--accent)" : "var(--text)" }}>
                    {u?.update_state?.state === "installing"
                      ? tr("正在安装 {0}…", [u.update_state.version])
                      : u?.update_state?.state === "done"
                        ? tr("已更新到 {0}，刷新页面后生效", [u.update_state.version])
                        : u?.update_state?.state === "failed"
                          ? tr("安装失败，请查看日志")
                          : !u
                            ? tr("软件更新")
                            : !u.result
                              ? tr("检查更新失败")
                              : hasNew
                                ? tr("发现新版本 {0}", [u.latest])
                                : tr("已是最新版本")}
                  </b>
                  <span class="muted small">
                    {u && !u.result ? serviceText(u.error || "") : u?.published_at ? tr("发布于 {0}", [u.published_at.slice(0, 10)]) : tr("获取最新功能与修复")}
                  </span>
                </span>
                {hasNew && u?.installable ? (
                  <button type="button" class="btn primary" disabled={!!busy.value} onClick={() => installUpdate()}>
                    {busy.value === "install" ? tr("安装中…") : tr("下载并安装")}
                  </button>
                ) : (
                  <button type="button" class="btn" disabled={!!busy.value} onClick={() => checkUpdate()}>
                    {busy.value === "check" ? tr("检查中…") : tr("检查更新")}
                  </button>
                )}
              </div>
              {hasNew && u?.notes && <pre class="mono small" style={{ margin: "8px 0 16px", whiteSpace: "pre-wrap", color: "#c8c8cc", maxHeight: "160px", overflow: "auto" }}>{serviceText(u.notes)}</pre>}
              <Row title={tr("每天自动检查更新")} desc={tr("打开页面时每天最多检查一次，有新版本会在顶部提示，不会自动安装")}>
                <Switch label={tr("每天自动检查更新")} checked={flag("update.auto_check")} onToggle={(on) => set("update.auto_check", on ? "1" : "0")} />
              </Row>
            </div>
            <div style={{ display: "flex", flexWrap: "wrap", gap: "4px 18px" }}>
              <a href="https://github.com/HyperPteLtd/openwrt-hyper" target="_blank" rel="noreferrer">GitHub</a>
              <a href="https://github.com/HyperPteLtd/openwrt-hyper/releases" target="_blank" rel="noreferrer">{tr("版本记录")}</a>
            </div>
          </section>
        </div>
      </div>

      <div style={{ position: "sticky", bottom: 0, display: "flex", flexWrap: "wrap", justifyContent: "flex-end", alignItems: "center", gap: "10px", padding: "14px 0", background: "var(--bg)", borderTop: "1px solid var(--control)" }}>
        <span class="muted" style={{ flexGrow: 1 }}>{changed.length ? tr("有 {0} 项未保存的修改", [changed.length]) : ""}</span>
        <button type="button" class="btn" disabled={!changed.length || isBusy} onClick={() => { draft.v = { ...saved.value }; language.value = languagePreference; }}>{tr("重置")}</button>
        <button type="button" class="btn primary" disabled={!changed.length || isBusy} onClick={() => save()}>{busy.value === "save" ? tr("保存中…") : tr("保存并应用")}</button>
      </div>
    </main>
  );
}
