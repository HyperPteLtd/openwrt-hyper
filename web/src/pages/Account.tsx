import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { Card, CardHeader, Notice } from "../components/ui";
import { app } from "../lib/app";
import { api, apiError, formatDate, money, type ApiResult } from "../lib/ppanel";
import { expiryText, localizedText } from "../lib/display";
import { call } from "../lib/rpc";
import { useSiteCurrency } from "../lib/site-currency";
import "./Account.css";

interface UserInfo {
  id?: number;
  nickname?: string;
  avatar?: string;
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
export function Account() {
  const currency = useSiteCurrency();
  const msg = useSignal("");
  const info = useSignal<UserInfo | null>(null);
  const subs = useSignal<UserSubscribe[]>([]);
  const devices = useSignal<UserDevice[]>([]);
  const affiliate = useSignal<{ registers?: number; total_commission?: number } | null>(null);
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
          ...[
            api<{ list: UserSubscribe[] }>("GET", "/v1/public/user/subscribe", { onUpdate: membership }).then(membership),
            api<{ list: UserDevice[] }>("GET", "/v1/public/user/devices", { onUpdate: deviceList }).then(deviceList),
          ],
        ]);
      } catch (e) {
        if (live) msg.value = (e as Error).message;
      }
    })();
    return () => { live = false; };
  }, [tick]);

  useEffect(() => {
    let live = true;
    (async () => {
      try {
        {
          const r = await api<{ registers: number; total_commission: number }>("GET", "/v1/public/user/affiliate/count", {
            onUpdate: (r) => { if (live && r.ok) affiliate.value = r.data ?? {}; },
          });
          if (!live) return;
          affiliate.value = r.ok ? r.data ?? {} : {};
        }

      } catch (e) {
        msg.value = (e as Error).message;
      }
    })();
    return () => { live = false; };
  }, [tick]);

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
  const trafficTotal = sync?.traffic_total === "不限" ? tr("不限") : sync?.traffic_total;

  return (
    <main class="page account-page">
      <Notice text={msg.value} />
      <div class="account-top-grid">
      <Card className="account-summary-grid" label={tr("账户与订阅")}>
        <section class="account-summary account-profile" aria-label={tr("账户与订阅")}>
          <div class="account-profile-heading">
            <span class="account-avatar" aria-hidden="true">{(name[0] || "?").toUpperCase()}</span>
            <div class="account-identity">
              <h2>{name || "--"}</h2>
              <span class="muted small">{app.status?.email}</span>
            </div>
            <button type="button" class="btn text danger-text account-heading-action account-logout" disabled={busy.value === "logout"} onClick={() => logout()}>{tr("退出登录")}</button>
          </div>
        </section>

        <section class="account-summary account-plan" aria-label={tr("套餐")}>
          <CardHeader
            title={localizedText(active?.subscribe?.name, hasSubs ? tr("套餐") : tr("还没有套餐"))}
            desc={active ? expiryText(active.expire_time, active.status) : tr("购买套餐后，这台路由器就能连接")}
          >
            <a href="#/plans" class="btn primary account-heading-action">{active ? tr("续费 / 升级") : tr("购买套餐")}</a>
          </CardHeader>
          {active && sync?.traffic_total && <dl class="account-metrics account-plan-metrics">
            <div><dt>{tr("已用流量")}</dt><dd>{sync.traffic_used}</dd></div>
            <div><dt>{tr("总流量")}</dt><dd>{trafficTotal}</dd></div>
            <div><dt>{tr("设备上限")}</dt><dd>{active?.subscribe?.device_limit || "--"}</dd></div>
          </dl>}
        </section>
      </Card>

      <Card className="account-invite" label={tr("邀请好友")}>
        <div class="account-invite-copy">
          <h2>{tr("邀请好友")}</h2>
          <p class="muted small">{tr("好友注册时填写你的邀请码，购买后你会获得返佣")}</p>
        </div>
        <div class="account-invite-code">
          {info.value?.refer_code ? <>
            <span class="mono">{info.value.refer_code}</span>
            <button type="button" class={`account-copy${copied.value ? " is-copied" : ""}`} aria-label={copied.value ? tr("已复制") : tr("复制")} title={copied.value ? tr("已复制") : tr("复制")} onClick={() => copyCode(info.value!.refer_code!)}>
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                {copied.value ? <path d="m5 12 4 4L19 6" /> : <><rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3" /></>}
              </svg>
            </button>
            <span class="account-copy-status" role="status">{copied.value ? tr("已复制") : ""}</span>
          </> : <span class="muted small">{tr("没有邀请码")}</span>}
        </div>
        <dl class="account-metrics account-invite-metrics">
          <div><dt>{tr("佣金")}</dt><dd>{money(info.value?.commission, currency)}</dd></div>
          <div><dt>{tr("已邀请")}</dt><dd>{affiliate.value?.registers ?? "--"}</dd></div>
          <div><dt>{tr("累计返佣")}</dt><dd>{money(affiliate.value?.total_commission, currency)}</dd></div>
        </dl>
      </Card>
      </div>

      <Card className="account-devices" label={tr("已登录的设备")}>
        <CardHeader title={tr("已登录的设备")} />
        <ul class="account-device-list">
          {devices.value.map((d) => <li key={d.id}>
            <span class="account-device-name">{d.current ? tr("本路由器") : d.platform || d.user_agent || tr("设备")}</span>
            <span class="account-device-ip">{d.ip || "--"}</span>
            <span class="account-device-status">{d.online ? tr("在线") : tr("离线")}</span>
            <span class="account-device-date">{d.last_login_at ? tr("登录于 {0}", [formatDate(d.last_login_at)]) : "--"}</span>
            <div class="account-device-action">{d.current ? <span class="small account-current">{tr("当前")}</span> :
              <button type="button" class="btn text danger-text" disabled={busy.value === `kick-${d.id}`} onClick={() => kick(d.id)}>{tr("下线")}</button>}</div>
          </li>)}
          {!devices.value.length && <li class="muted small account-devices-empty">{tr("没有设备记录")}</li>}
        </ul>
      </Card>

    </main>
  );
}
