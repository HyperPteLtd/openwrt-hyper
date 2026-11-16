import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { Modal } from "./Modal";
import { CheckIn } from "../pages/CheckIn";
import { useEffect, useRef } from "preact/hooks";
import { app, ROUTES, tabOf } from "../lib/app";
import { operationPending } from "../lib/service";
import { AutosaveStatus } from "./AutosaveStatus";

export const Logo = ({ size = 28 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 26 26" aria-hidden="true">
    <path d="M9 2h15l-6 6H3z" fill="var(--accent)" />
    <path d="M9 10h15l-6 6H3z" fill="var(--accent)" />
    <path d="M9 18h11l-6 6H3z" fill="var(--accent)" />
  </svg>
);

export function TopBar() {
  const checkin = useSignal(false);
  const s = app.status;
  const live = app.liveStatus;
  const tone = !live || !s ? "var(--muted)" : s.health?.state === "failopen" ? "var(--warn)" : s.running ? "var(--accent)" : s.enabled ? "var(--danger-text)" : "var(--muted)";
  const tab = tabOf(app.route);
  const nav = useRef<HTMLElement>(null);
  const pending = app.controlAction || (operationPending(s?.operation) ? s?.operation?.action : null);
  const label = !live ? tr("正在连接路由器…") : !s ? tr("加载中") : pending ? pending === "stop" ? tr("正在停止…") : pending === "restart" ? tr("正在重启…") : tr("正在启动…") : s.health?.state === "failopen" ? tr("已临时直连") : s.running ? tr("运行中") : s.enabled ? tr("连接失败") : tr("已停止");

  useEffect(() => {
    const bar = nav.current;
    const selected = bar?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!bar || !selected) return;
    const reveal = () => {
      const bounds = bar.getBoundingClientRect();
      const item = selected.getBoundingClientRect();
      if (item.left < bounds.left) bar.scrollLeft -= bounds.left - item.left + 4;
      else if (item.right > bounds.right) bar.scrollLeft += item.right - bounds.right + 4;
    };
    reveal();
    const ro = new ResizeObserver(reveal);
    ro.observe(bar);
    return () => ro.disconnect();
  }, [tab]);

  return (
    <header class="topbar">
      <div class="topbar-identity">
        <a class="brand" href="#/overview" aria-label="Hyper"><Logo /><h1>Hyper</h1></a>
        <span class="pill service-status" style={{ color: tone }}><span class="dot" />{label}</span>
        {s?.update?.available && <a href="#/settings" class="pill" style={{ color: "var(--accent)" }}>{tr("有新版本")} {s.update.latest}</a>}
        {s?.signed_in && <button type="button" class="btn ghost checkin-trigger" aria-haspopup="dialog" onClick={() => (checkin.value = true)}>
          <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="16" rx="3" /><path d="M7 3v4m10-4v4M3 10h18m-13 5 3 3 5-5" /></svg>
          {tr("每日签到")}
        </button>}
        {s?.signed_in && s.email && <a href="#/account" class="pill account-link" title={s.email}>{s.email}</a>}
      </div>
      <div class="topbar-navigation">
        <nav ref={nav} class="primary-nav" aria-label={tr("Hyper 页面")}>
          {ROUTES.map((r) => <a key={r.id} href={`#/${r.id}`} aria-current={tab === r.id ? "page" : undefined}>{r.label}</a>)}
        </nav>
        <AutosaveStatus />
      </div>
      <Modal open={checkin.value && !!s?.signed_in} title={tr("每日签到")} desc={tr("每天来看看，记录连续签到的天数")} onClose={() => (checkin.value = false)}><CheckIn embedded /></Modal>
    </header>
  );
}
