import { t as tr } from "./i18n";
import { createStore } from "./store";
import type { ServiceOperation, Status } from "./types";

export type Route = "overview" | "account" | "plans" | "diagnostics" | "settings";

export const ROUTES: { id: Route; label: string }[] = [
  { id: "overview", label: tr("概览") },
  { id: "account", label: tr("账户与订阅") },
  { id: "diagnostics", label: tr("诊断与日志") },
  { id: "settings", label: tr("设置") },
];

export interface AppState {
  route: Route;
  status: Status | null;
  statusAt: number;
  /** True after the router responds in this page session, never restored from cache. */
  liveStatus: boolean;
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
export const app = createStore<AppState>({ route: routeFromHash(location.hash), status: null, statusAt: 0, liveStatus: false, controlAction: null, loading: true, error: "", tick: 0 });
