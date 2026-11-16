import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { Notice, Segmented } from "../components/ui";
import { app } from "../lib/app";
import { api, apiError, formatDate, money, type ApiResult } from "../lib/ppanel";
import { expiryText, localizedText } from "../lib/display";
import { call } from "../lib/rpc";
import { CheckIn } from "./CheckIn";

interface UserInfo {
  id?: number;
  nickname?: string;
  avatar?: string;
  balance?: number;
  commission?: number;
  refer_code?: string;
}
interface UserSubscribe {
  id: number;
  status: number;
  expire_time: number | string | null;
  start_time: number;
  subscribe?: { name?: string | Record<string, string>; device_limit?: number; traffic?: number } | null;
}
interface UserDevice {
  id: number;
  identifier?: string;
  ip?: string;
  platform?: string;
  user_agent?: string;
  online?: boolean;
  current?: boolean;
  last_login_at?: number;
}
interface Announcement {
  id: number;
  title: string;
  content: string;
  created_at: number;
  pinned?: boolean;
}

type Tab = "profile" | "checkin" | "invite" | "messages";

export function Account() {
  const tab = useSignal<Tab>("profile");
  const msg = useSignal("");
  const info = useSignal<UserInfo | null>(null);
  const subs = useSignal<UserSubscribe[]>([]);
  const devices = useSignal<UserDevice[]>([]);
  const affiliate = useSignal<{ registers?: number; total_commission?: number } | null>(null);
  const notices = useSignal<Announcement[]>([]);
  const openNotice = useSignal<number | null>(null);
  const busy = useSignal("");
  const copied = useSignal(false);

  const tick = app.tick;
  useEffect(() => {
    let live = true;
    const user = (r: ApiResult<UserInfo>) => { if (live) { if (r.ok) info.value = r.data ?? null; else msg.value = apiError(r); } };
    const membership = (r: ApiResult<{ list: UserSubscribe[] }>) => { if (live && r.ok) subs.value = r.data?.list ?? []; };
    const deviceList = (r: ApiResult<{ list: UserDevice[] }>) => { if (live && r.ok) devices.value = r.data?.list ?? []; };
    (async () => {
      try {
        await Promise.all([
          api<UserInfo>("GET", "/v1/public/user/info", { onUpdate: user }).then(user),
          api<{ list: UserSubscribe[] }>("GET", "/v1/public/user/subscribe", { onUpdate: membership }).then(membership),
          api<{ list: UserDevice[] }>("GET", "/v1/public/user/devices", { onUpdate: deviceList }).then(deviceList),
        ]);
      } catch (e) {
        if (live) msg.value = (e as Error).message;
      }
    })();
    return () => { live = false; };
  }, [tick]);

  const t = tab.value;
  useEffect(() => {
    let live = true;
    (async () => {
      try {
        if (t === "invite" && !affiliate.value) {
          const r = await api<{ registers: number; total_commission: number }>("GET", "/v1/public/user/affiliate/count", {
            onUpdate: (r) => { if (live && r.ok) affiliate.value = r.data ?? {}; },
          });
          if (!live) return;
          affiliate.value = r.ok ? r.data ?? {} : {};
        }
        if (t === "messages" && !notices.value.length) {
          const r = await api<{ announcements: Announcement[] }>("GET", "/v1/public/announcement/list", { query: { page: 1, size: 30 }, onUpdate: (r) => { if (live && r.ok) notices.value = r.data?.announcements ?? []; } });
          if (!live) return;
          notices.value = r.ok ? r.data?.announcements ?? [] : [];
          if (notices.value.length) openNotice.value = notices.value[0].id;
        }
      } catch (e) {
        msg.value = (e as Error).message;
      }
    })();
    return () => { live = false; };
  }, [t]);

  const kick = async (id: number) => {
    busy.value = `kick-${id}`;
    const r = await api("PUT", "/v1/public/user/device/kick_offline", { body: { id } });
    if (!r.ok) msg.value = apiError(r, tr("下线失败"));
    busy.value = "";
    app.tick++;
  };

  const logout = async () => {
    if (!confirm(tr("在这台路由器上退出登录？代理会停止，局域网恢复直连"))) return;
    busy.value = "logout";
    await call("logout");
    busy.value = "";
    app.tick++;
  };

  const copyCode = async (code: string) => {
    try {
      await navigator.clipboard.writeText(code);
      copied.value = true;
      setTimeout(() => (copied.value = false), 1500);
    } catch {
      /* clipboard needs https; the code is visible anyway */
    }
  };

  const active = subs.value.find((s) => s.status === 1) ?? subs.value[0];
  const sync = app.status?.sync as (Record<string, unknown> & { traffic_used?: string; traffic_total?: string }) | null | undefined;
  const name = info.value?.nickname || app.status?.email || "";
  const hasSubs = subs.value.length > 0;

  return (
    <main class="page">
      <Segmented
        label={tr("账户")}
        value={tab.value}
        options={[{ id: "profile", label: tr("资料与套餐") }, { id: "checkin", label: tr("每日签到") }, { id: "invite", label: tr("邀请好友") }, { id: "messages", label: tr("消息") }]}
        onChange={(id) => (tab.value = id as Tab)}
      />
      <Notice text={msg.value} />

      {tab.value === "profile" && (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "1 1 320px" }}>
            <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
              <span style={{ width: "56px", height: "56px", borderRadius: "28px", background: "var(--control)", display: "inline-flex", alignItems: "center", justifyContent: "center", fontSize: "22px", fontWeight: 600 }}>
                {(name[0] || "?").toUpperCase()}
              </span>
              <span style={{ display: "flex", flexDirection: "column", gap: "4px", minWidth: 0 }}>
                <b style={{ fontSize: "20px", overflowWrap: "anywhere" }}>{name || "--"}</b>
                <span class="muted small">{app.status?.email}</span>
              </span>
            </div>
            <dl class="grid-stats" style={{ margin: 0 }}>
              <div class="stat"><b>{money(info.value?.balance)}</b><span>{tr("余额")}</span></div>
              <div class="stat"><b>{money(info.value?.commission)}</b><span>{tr("佣金")}</span></div>
            </dl>
            <button type="button" class="btn" style={{ color: "var(--danger-text)" }} disabled={busy.value === "logout"} onClick={() => logout()}>{tr("在这台路由器上退出登录")} </button>
          </section>

          <div style={{ flex: "2 1 460px", display: "flex", flexDirection: "column", gap: "20px", minWidth: 0 }}>
            <section class="card">
              <span class="small" style={{ color: "var(--accent)", fontWeight: 600, letterSpacing: "0.4px" }}>HYPER VIP</span>
              <b style={{ fontSize: "24px" }}>{localizedText(active?.subscribe?.name, hasSubs ? tr("套餐") : tr("还没有套餐"))}</b>
              {active ? (
                <span class="muted">
                  {expiryText(active.expire_time, active.status)}
                </span>
              ) : (
                <span class="muted">{tr("购买套餐后，这台路由器就能连接")}</span>
              )}
              {sync?.traffic_total && (
                <dl class="grid-stats" style={{ margin: 0 }}>
                  <div class="stat"><b>{sync.traffic_used}</b><span>{tr("已用流量")}</span></div>
                  <div class="stat"><b>{sync.traffic_total}</b><span>{tr("总流量")}</span></div>
                  <div class="stat"><b>{active?.subscribe?.device_limit || "--"}</b><span>{tr("设备上限")}</span></div>
                </dl>
              )}
              <div style={{ display: "flex", flexWrap: "wrap", gap: "10px" }}>
                <a href="#/plans" class="btn primary">{active ? tr("续费 / 升级") : tr("购买套餐")}</a>
              </div>
            </section>

            <section class="card">
              <h2>{tr("已登录的设备")}</h2>
              <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                {devices.value.map((d) => (
                  <li key={d.id} style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "12px", minHeight: "60px", borderTop: "1px solid var(--line)" }}>
                    <span style={{ flex: "1 1 200px", display: "flex", flexDirection: "column", gap: "3px", minWidth: 0 }}>
                      <span>{d.current ? tr("本路由器") : d.platform || d.user_agent || tr("设备")}</span>
                      <span class="muted small">
                        {[d.ip, d.online ? tr("在线") : tr("离线"), d.last_login_at ? tr("登录于 {0}", [formatDate(d.last_login_at)]) : ""].filter(Boolean).join(" · ")}
                      </span>
                    </span>
                    {d.current ? (
                      <span class="small" style={{ color: "var(--accent)" }}>{tr("当前")}</span>
                    ) : (
                      <button type="button" class="btn" style={{ minHeight: "36px" }} disabled={busy.value === `kick-${d.id}`} onClick={() => kick(d.id)}>{tr("下线")} </button>
                    )}
                  </li>
                ))}
                {!devices.value.length && <li class="muted" style={{ padding: "12px 0" }}>{tr("没有设备记录")}</li>}
              </ul>
            </section>
          </div>
        </div>
      )}

      {tab.value === "checkin" && <CheckIn />}

      {tab.value === "invite" && (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "1 1 360px" }}>
            <h2 style={{ fontSize: "22px" }}>{tr("邀请好友")}</h2>
            <p class="muted" style={{ margin: 0 }}>{tr("好友注册时填写你的邀请码，购买后你会获得返佣")}</p>
            {info.value?.refer_code ? (
              <div style={{ display: "flex", alignItems: "center", gap: "10px", minHeight: "52px", padding: "0 8px 0 16px", borderRadius: "14px", background: "var(--control)" }}>
                <span class="mono" style={{ flexGrow: 1, fontSize: "20px", fontWeight: 600, letterSpacing: "2px" }}>{info.value.refer_code}</span>
                <button type="button" class="btn primary" style={{ minHeight: "40px" }} onClick={() => copyCode(info.value!.refer_code!)}>
                  {copied.value ? tr("已复制") : tr("复制")}
                </button>
              </div>
            ) : (
              <span class="muted">{tr("没有邀请码")}</span>
            )}
          </section>
          <section class="card" style={{ flex: "1 1 360px" }}>
            <dl class="grid-stats" style={{ margin: 0 }}>
              <div class="stat"><b style={{ fontSize: "26px" }}>{affiliate.value?.registers ?? "--"}</b><span>{tr("已邀请")}</span></div>
              <div class="stat"><b style={{ fontSize: "26px" }}>{money(affiliate.value?.total_commission)}</b><span>{tr("累计返佣")}</span></div>
            </dl>
          </section>
        </div>
      )}

      {tab.value === "messages" &&
        (notices.value.length ? (
          <div class="row" style={{ alignItems: "flex-start" }}>
            <ul class="card" style={{ flex: "1 1 320px", listStyle: "none", margin: 0, padding: "8px", gap: "4px" }}>
              {notices.value.map((n) => (
                <li key={n.id}>
                  <button
                    type="button"
                    class="option"
                    aria-pressed={openNotice.value === n.id}
                    style={{ width: "100%", flexDirection: "column", alignItems: "flex-start", border: 0, background: openNotice.value === n.id ? "var(--control)" : "transparent" }}
                    onClick={() => (openNotice.value = n.id)}
                  >
                    <b>{n.pinned ? tr("置顶 · ") : ""}{n.title}</b>
                    <span class="muted small">{formatDate(n.created_at)}</span>
                  </button>
                </li>
              ))}
            </ul>
            {(() => {
              const n = notices.value.find((x) => x.id === openNotice.value);
              return n ? (
                <article class="card" style={{ flex: "2 1 460px", minHeight: "320px" }}>
                  <h2 style={{ fontSize: "22px" }}>{n.title}</h2>
                  <span class="muted small">{formatDate(n.created_at)}</span>
                  {/* announcements may contain markdown or HTML; shown as plain text on purpose */}
                  <p style={{ margin: 0, lineHeight: 1.7, whiteSpace: "pre-line", color: "var(--text-2)" }}>{n.content}</p>
                </article>
              ) : null;
            })()}
          </div>
        ) : (
          <section class="card empty">
            <b style={{ fontSize: "18px" }}>{tr("暂时没有消息")}</b>
            <span class="muted">{tr("服务公告会出现在这里")}</span>
          </section>
        ))}
    </main>
  );
}
