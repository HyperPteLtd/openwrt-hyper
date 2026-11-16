import { t as tr } from "./i18n";
import { createStore } from "./store";
import type { ServiceOperation, Status } from "./types";

export type Route = "overview" | "routing" | "devices" | "account" | "plans" | "logs" | "settings";

export const ROUTES: { id: Route; label: string }[] = [
  { id: "overview", label: tr("概览") },
  { id: "routing", label: tr("分流与 DNS") },
  { id: "devices", label: tr("接入设备") },
  { id: "account", label: tr("账户与订阅") },
  { id: "logs", label: tr("日志与诊断") },
  { id: "settings", label: tr("设置") },
];

export interface AppState {
  route: Route;
  status: Status | null;
  statusAt: number;
  controlAction: ServiceOperation["action"] | null;
  loading: boolean;
  error: string;
  /** bumped to ask pages to reload their data */
  tick: number;
}

/** Pages reached from inside another tab (no tab of their own) and the tab they belong to */
export const SUB_ROUTES: Partial<Record<Route, Route>> = { plans: "account" };

export function routeFromHash(hash: string): Route {
  const id = hash.replace(/^#\/?/, "").split("/")[0] as Route;
  return ROUTES.some((r) => r.id === id) || id in SUB_ROUTES ? id : "overview";
}

export function tabOf(route: Route): Route {
  return SUB_ROUTES[route] ?? route;
}

/** App-wide state: the current tab, the router status (polled by Root) and the reload tick */
export const app = createStore<AppState>({ route: routeFromHash(location.hash), status: null, statusAt: 0, controlAction: null, loading: true, error: "", tick: 0 });
