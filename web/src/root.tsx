import type { VNode } from "preact";
import { useEffect } from "preact/hooks";
import { TopBar } from "./components/TopBar";
import { app, type Route, routeFromHash } from "./lib/app";
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
  /* Hash routing (#/nodes ...), iframe height reporting and status polling */
  useEffect(() => {
    const onHash = () => {
      app.route = routeFromHash(location.hash);
      window.scrollTo(0, 0);
    };
    window.addEventListener("hashchange", onHash);

    const ro = new ResizeObserver(() => reportHeight());
    ro.observe(document.body);

    /* Refresh status every 5 s so start/stop and watchdog changes show up */
    const id = setInterval(() => app.tick++, 5000);
    return () => {
      window.removeEventListener("hashchange", onHash);
      ro.disconnect();
      clearInterval(id);
    };
  }, []);

  const tick = app.tick;
  useEffect(() => {
    call<Status>("status")
      .then((s) => {
        app.status = s;
        app.error = "";
      })
      .catch((e: Error) => (app.error = e.message))
      .finally(() => (app.loading = false));
  }, [tick]);

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
