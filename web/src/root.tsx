import { t as tr } from "./lib/i18n";
import type { VNode } from "preact";
import { useEffect } from "preact/hooks";
import { TopBar } from "./components/TopBar";
import { app, type Route, routeFromHash } from "./lib/app";
import { usePoll } from "./lib/poll";
import { call, isMock, reportHeight, readCachedStatus, rememberStatus } from "./lib/rpc";
import type { Status } from "./lib/types";
import { operationPending } from "./lib/service";
import { Account } from "./pages/Account";
import { Login } from "./pages/Login";
import { Logs } from "./pages/Logs";
import { Overview } from "./pages/Overview";
import { Plans } from "./pages/Plans";
import { Settings } from "./pages/Settings";

import "./global.css";

const cachedStatus = readCachedStatus();
if (cachedStatus) app.status = cachedStatus;

export function Root() {
  /* Hash routing and iframe height reporting */
  useEffect(() => {
    const onHash = () => {
      app.route = routeFromHash(location.hash);
      window.scrollTo(0, 0);
    };
    window.addEventListener("hashchange", onHash);

    const ro = new ResizeObserver(() => reportHeight());
    ro.observe(document.body);
    return () => {
      window.removeEventListener("hashchange", onHash);
      ro.disconnect();
    };
  }, []);

  /*
   * Status every 5 s so start/stop and watchdog changes show up (only while the
   * page is visible), and right after an action bumps app.tick. Pages reload
   * their own data on app.tick, not on this poll.
   */
  const loadStatus = () => {
    const operationAtRequest = app.status?.operation?.id;
    return call<Status>("status")
      .then((s) => {
        // A response started before the control acknowledgment must not erase it.
        if (app.status?.operation?.id !== operationAtRequest) return;
        rememberStatus(s);
        app.liveStatus = true;
        app.status = s;
        app.statusAt = performance.now();
        app.error = "";
      })
      .catch((e: Error) => (app.error = e.message))
      .finally(() => (app.loading = false));
  };
  usePoll(loadStatus, operationPending(app.status?.operation) ? 1000 : 5000);
  const tick = app.tick;
  useEffect(() => {
    if (tick) loadStatus();
  }, [tick]);


  const s = app.status;
  let page;
  if (app.error && !s) page = <main class="page"><div class="banner">{tr("无法连接路由器：")}{app.error}</div></main>;
  else if (!s) page = <main class="page muted">{tr("加载中…")}</main>;
  else if (!s.signed_in && app.route !== "settings") page = <Login />;
  else page = PAGES[app.route]();

  return (
    <>
      {isMock() && (
        <div style={{ background: "#1f1a0e", color: "#ffd27a", fontSize: "12px", padding: "6px 32px" }}>{tr("演示模式：不在 LuCI 里打开，使用的是模拟数据")} </div>
      )}
      <TopBar />
      {page}
    </>
  );
}

const PAGES: Record<Route, () => VNode> = {
  overview: () => <Overview />,
  account: () => <Account />,
  plans: () => <Plans />,
  diagnostics: () => <Logs />,
  settings: () => <Settings />,
};
