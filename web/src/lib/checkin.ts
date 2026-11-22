import { t as tr } from "./i18n";
/**
 * Daily check-in. Hyper service has no API for it, so the calendar is kept on the router
 * (rpcd checkin / checkin_status, per account), the same way the Hyper app keeps
 * its check-in calendar on the phone. Dates are the browser's local dates.
 */
import { call } from "./rpc";
import { errorText } from "./errors";

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
  return errorText(r.error, tr("签到失败"));
}
