import { api } from "./api";
import { call } from "./rpc";
import { warmAccount } from "./warm";

/*
 * The account reads the pages make on first open, fetched right after sign-in so the
 * account, plan and check-in views open from the read cache instead of waiting on the
 * Hyper service. The arguments match the pages' own calls exactly, so a page opened while
 * one is in flight joins it. One at a time: each is a task on the router.
 */
const READS: (() => Promise<unknown>)[] = [
  () => api("GET", "/v1/public/user/info"),
  () => api("GET", "/v1/public/user/subscribe"),
  () => api("GET", "/v1/common/site/config"),
  () => api("GET", "/v1/public/announcement/list", { query: { page: 1, size: 30 } }),
  () => call("checkin_status"),
  () => api("GET", "/v1/public/user/devices"),
  () => api("GET", "/v1/public/user/affiliate/count"),
  () => api("GET", "/v1/public/subscribe/list"),
  () => api("GET", "/v1/public/payment/methods"),
];

export function warmSignedIn(account: string, current: () => string): Promise<void> {
  return warmAccount(account, current, READS);
}
