import { t as tr } from "../lib/i18n";
import "./Settings.css";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { useStore } from "../lib/store";
import { Card, CardHeader, Notice, Row, Switch } from "../components/ui";
import { useAutosave } from "../components/AutosaveStatus";
import { configWrites, flushPreferences, pendingLanguagePreference, requestLanguage } from "../lib/preferences";
import { app } from "../lib/app";
import { formatAgo, formatVersion } from "../lib/format";
import { serviceText } from "../lib/display";
import { languagePreference, type LanguagePreference } from "../lib/i18n";
import { call } from "../lib/rpc";
import type { Result, SoftwareUpdateState, Status } from "../lib/types";
import { errorText } from "../lib/errors";

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
  update_state?: SoftwareUpdateState | null;
}

const LOG_LEVELS = [
  { id: "error", label: tr("错误") },
  { id: "warn", label: tr("警告") },
  { id: "info", label: tr("信息") },
  { id: "debug", label: tr("调试") },
];

export function Settings() {
  const language = useSignal<LanguagePreference>(pendingLanguagePreference ?? languagePreference);
  const loaded = useSignal(false);
  const updateRequest = useRef(0);
  const active = useRef(true);
  useAutosave(configWrites);
  const draft = useStore<{ v: Config }>({ v: {} });
  const msg = useStore({ text: "", tone: "error" as "error" | "warn" | "ok" });
  const busy = useSignal("");
  const update = useSignal<UpdateInfo | null>(null);

  useEffect(() => {
    active.current = true;
    const revision = configWrites.revision;
    const pendingAtRead = configWrites.value;
    const apply = (r: { config: Config }) => {
      if (!active.current) return;
      if (revision === configWrites.revision) draft.v = { ...r.config, ...pendingAtRead, ...configWrites.value };
      loaded.value = true;
    };
    call<{ config: Config }>("get_config", {}, { onUpdate: apply })
      .then(apply)
      .catch((e: Error) => { if (active.current) msg.text = e.message; });
    return () => { active.current = false; updateRequest.current++; };
  }, []);

  const set = (key: string, value: string) => {
    draft.v = { ...draft.v, [key]: value };
    configWrites.enqueue({ [key]: value });
  };
  const flag = (key: string) => draft.v[key] === "1";
  const disabled = !loaded.value || !app.liveStatus;

  const changeLanguage = async (value: LanguagePreference) => {
    language.value = value;
    try {
      await requestLanguage(value);
    } catch (e) {
      if (!active.current) return;
      language.value = languagePreference;
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
  };

  const checkUpdate = async (fresh = true) => {
    const request = ++updateRequest.current;
    busy.value = "check";
    try {
      const result = await call<UpdateInfo>("check_update", {}, { fresh });
      if (active.current && request === updateRequest.current) update.value = result;
    } catch (e) {
      if (active.current && request === updateRequest.current) update.value = { result: false, error: (e as Error).message };
    }
    if (active.current && request === updateRequest.current) busy.value = "";
  };

  const installUpdate = async () => {
    if (busy.value === "install") return;
    updateRequest.current++;
    busy.value = "install";
    msg.text = "";
    try {
      await flushPreferences();
      const r = await call<Result & { started?: boolean; version?: string }>("install_update");
      if (!r.result) {
        msg.text = r.error === "up_to_date" ? errorText(r.error) : tr("更新失败：{0}", [errorText(r.error, tr("未知原因"))]);
        msg.tone = r.error === "up_to_date" ? "ok" : "error";
        busy.value = "";
        return;
      }
      update.value = {
        ...update.value, result: true,
        update_state: { state: "installing", version: r.version || update.value?.latest || "", time: Math.floor(Date.now() / 1000) },
      };
      /* the package replaces the backend; poll until it reports done/failed */
      for (let i = 0; i < 60 && active.current; i++) {
        await new Promise((res) => setTimeout(res, 3000));
        if (!active.current) break;
        try {
          const status = await call<Status>("status");
          const state = status.update?.state;
          update.value = {
            ...update.value, result: true, current: status.version,
            update_available: status.update?.available ?? update.value?.update_available,
            update_state: state,
          };
          if (state && state.state !== "installing") {
            app.tick++;
            break;
          }
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
  const liveUpdate = s?.update?.state;
  const updateState = liveUpdate && (!u?.update_state || liveUpdate.time > u.update_state.time) ? liveUpdate : u?.update_state;
  const installing = busy.value === "install" || updateState?.state === "installing";
  const updateFailed = updateState?.state === "failed" || !!(u && !u.result);
  const updateMessage = updateState?.state === "installing"
    ? tr("正在安装 {0}…", [formatVersion(updateState.version)])
    : updateState?.state === "done"
      ? tr("已更新到 {0}，刷新页面后生效", [formatVersion(updateState.version)])
      : updateState?.state === "failed"
        ? tr("安装失败，请查看日志")
        : !u ? "" : !u.result ? tr("检查更新失败") : hasNew ? tr("发现新版本 {0}", [formatVersion(u.latest)]) : tr("已是最新版本");
  const updateDetail = updateFailed
    ? errorText(updateState?.error || u?.error)
    : u?.published_at ? tr("发布于 {0}", [u.published_at.slice(0, 10)]) : "";

  return (
    <main class="page settings-page">
      <Notice text={msg.text} tone={msg.tone} />
      <div class="settings-layout">
        <Card className="settings-general">
          <CardHeader title={tr("常规设置")} />
          <div class="settings-fields">
            <Row title={tr("界面语言")} desc={tr("默认跟随路由器的 LuCI 语言，选择后立即生效")}>
              <select class="input settings-select" aria-label={tr("界面语言")} value={language.value} disabled={disabled} onChange={(e) => { void changeLanguage(e.currentTarget.value as LanguagePreference); }}>
                <option value="auto">{tr("跟随路由器")}</option>
                <option value="zh-CN">{tr("简体中文")}</option>
                <option value="en-US">English</option>
              </select>
            </Row>
            <Row title={tr("路由器自身流量走代理")} desc={tr("比如路由器上的软件更新、节点同步")}>
              <Switch disabled={disabled} label={tr("路由器自身流量走代理")} checked={draft.v["control.proxy_router_self"] !== "0"} onToggle={(on) => set("control.proxy_router_self", on ? "1" : "0")} />
            </Row>
            <Row title={tr("日志级别")}>
              <select aria-label={tr("日志级别")} class="input settings-select" value={draft.v["config.log_level"] || "warn"} disabled={disabled} onChange={(e) => set("config.log_level", e.currentTarget.value)}>
                {LOG_LEVELS.map((level) => <option key={level.id} value={level.id}>{level.label}</option>)}
              </select>
            </Row>
          </div>
        </Card>

        <div class="settings-column">
          <Card className="settings-nodes">
            <CardHeader title={tr("节点更新")} />
            <div class="settings-fields">
              <Row title={tr("自动同步节点")} desc={tr("每天在设定的时间同步一次 · 上次 {0}", [formatAgo(s?.sync?.time)])}>
                <Switch disabled={disabled} label={tr("自动同步节点")} checked={flag("subscription.auto_update")} onToggle={(on) => set("subscription.auto_update", on ? "1" : "0")} />
              </Row>
              <Row title={tr("同步时间")} desc={tr("路由器本地时间")}>
                <select aria-label={tr("同步时间")} class="input settings-select" value={draft.v["subscription.auto_update_time"] ?? "4"} disabled={disabled} onChange={(e) => set("subscription.auto_update_time", e.currentTarget.value)}>
                  {Array.from({ length: 24 }, (_, h) => <option key={h} value={String(h)}>{`${String(h).padStart(2, "0")}:00`}</option>)}
                </select>
              </Row>
            </div>
          </Card>

          <Card className="settings-software">
            <CardHeader title={tr("软件更新")} desc={`${tr("版本")} ${formatVersion(u?.current || s?.version)}`}>
              {hasNew && u?.installable ?
                <button type="button" class="btn primary settings-update-button" disabled={installing || !app.liveStatus} onClick={() => installUpdate()}>{installing ? tr("安装中…") : tr("下载并安装")}</button> :
                <button type="button" class="btn settings-update-button" aria-busy={busy.value === "check"} disabled={installing} onClick={() => checkUpdate()}>{installing ? tr("安装中…") : busy.value === "check" ? tr("检查中…") : tr("检查更新")}</button>}
            </CardHeader>
            {updateMessage && <div class="settings-update-state" data-state={updateFailed ? "error" : updateState?.state || (hasNew ? "available" : "current")} role={updateFailed ? "alert" : "status"}>
              <strong>{updateMessage}</strong>
              {updateDetail && <span class="muted small">{updateDetail}</span>}
            </div>}
            {hasNew && u?.notes && <pre class="settings-release-notes mono small">{serviceText(u.notes)}</pre>}
            <div class="settings-fields">
              <Row title={tr("自动检查新版本")} desc={tr("每天检查一次，有新版本时提示")}>
                <Switch disabled={disabled} label={tr("自动检查新版本")} checked={flag("update.auto_check")} onToggle={(on) => set("update.auto_check", on ? "1" : "0")} />
              </Row>
              <Row title={tr("自动更新")} desc={tr("每天检查并自动安装新版本，更新时连接可能短暂中断")}>
                <Switch disabled={disabled} label={tr("自动更新")} checked={flag("update.auto_update")} onToggle={(on) => set("update.auto_update", on ? "1" : "0")} />
              </Row>
            </div>
            <div class="settings-links">
              <a href="https://github.com/HyperPteLtd/openwrt-hyper" target="_blank" rel="noreferrer">GitHub</a>
              <a href="https://github.com/HyperPteLtd/openwrt-hyper/releases" target="_blank" rel="noreferrer">{tr("版本记录")}</a>
            </div>
          </Card>
        </div>
      </div>
    </main>
  );
}
