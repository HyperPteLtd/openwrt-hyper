/**
 * Daily check-in. PPanel has no API for it, so the calendar is kept on the router
 * (rpcd checkin / checkin_status, per account), the same way the Hyper app keeps
 * its check-in calendar on the phone. Dates are the browser's local dates.
 */
import { call } from "./rpc";

export * from "./calendar";

export interface CheckinReply {
  result: boolean;
  dates?: string[];
  added?: boolean;
  error?: string;
}

export function checkinStatus() {
  return call<CheckinReply>("checkin_status");
}

export function checkIn(date: string) {
  return call<CheckinReply>("checkin", { date });
}

export function checkinError(r: CheckinReply): string {
  if (r.error === "not signed in") return "请先登录账户。";
  if (r.error === "illegal date") return "这台电脑和路由器的日期相差太多，请先校准时间。";
  if (r.error === "write failed") return "路由器存储写入失败，请稍后再试。";
  return r.error || "签到失败";
}
