import type { VNode } from "preact";
import { useEffect } from "preact/hooks";
import { TopBar } from "./components/TopBar";
import { app, type Route, routeFromHash } from "./lib/app";
import { usePoll } from "./lib/poll";
import { call, isMock, reportHeight } from "./lib/rpc";
import type { Status } from "./lib/types";
import { Account } from "./pages/Account";
import { Devices } from "./pages/Devices";
import { Login } from "./pages/Login";
import { Logs } from "./pages/Logs";
import { Nodes } from "./pages/Nodes";
import { Overview } from "./pages/Overview";
import { Plans } from "./pages/Plans";
import { Routing } from "./pages/Routing";
import { Settings } from "./pages/Settings";

import "./global.css";

export function Root() {
  /* Hash routing (#/nodes ...) and iframe height reporting */
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
  const loadStatus = () =>
    call<Status>("status")
      .then((s) => {
        app.status = s;
        app.error = "";
      })
      .catch((e: Error) => (app.error = e.message))
      .finally(() => (app.loading = false));
  usePoll(loadStatus, 5000);
  const tick = app.tick;
  useEffect(() => {
    if (tick) loadStatus();
  }, [tick]);

  /* "Check for updates daily": at most once a day, when the page is opened */
  const update = app.status?.update;
  useEffect(() => {
    if (!update?.auto_check || autoChecked) return;
    if (update.checked && Date.now() / 1000 - update.checked < 86400) return;
    autoChecked = true;
    call("check_update")
      .then(loadStatus)
      .catch(() => {
        /* shown again tomorrow */
      });
  }, [update?.auto_check, update?.checked]);

  const s = app.status;
  let page;
  if (app.error && !s) page = <main class="page"><div class="banner">无法连接路由器：{app.error}</div></main>;
  else if (!s) page = <main class="page muted">加载中…</main>;
  else if (!s.signed_in && app.route !== "settings") page = <Login />;
  else page = PAGES[app.route]();

  return (
    <>
      {isMock() && (
        <div style={{ background: "#1f1a0e", color: "#ffd27a", fontSize: "12px", padding: "6px 32px" }}>
          演示模式：不在 LuCI 里打开，使用的是模拟数据。
        </div>
      )}
      <TopBar />
      {page}
    </>
  );
}

/* one automatic update check per page load */
let autoChecked = false;

const PAGES: Record<Route, () => VNode> = {
  overview: () => <Overview />,
  nodes: () => <Nodes />,
  routing: () => <Routing />,
  devices: () => <Devices />,
  account: () => <Account />,
  plans: () => <Plans />,
  logs: () => <Logs />,
  settings: () => <Settings />,
};
