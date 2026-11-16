/** Account membership totals are bytes from /v1/public/user/subscribe. */
export interface MembershipCounters {
  traffic?: number | string | null;
  upload?: number | string | null;
  download?: number | string | null;
  subscribe?: { device_limit?: number | null } | null;
}

function nonnegative(value: unknown): number | null {
  const parsed = typeof value === "number" ? value : typeof value === "string" && value.trim() ? Number(value) : NaN;
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

/** Missing counters are unknown; only an explicit zero represents zero usage. */
export function membershipCounters(value: MembershipCounters | undefined) {
  const upload = nonnegative(value?.upload), download = nonnegative(value?.download);
  const used = upload !== null && download !== null ? upload + download : null;
  const devices = nonnegative(value?.subscribe?.device_limit);
  return {
    used: used !== null && Number.isFinite(used) ? used : null,
    total: nonnegative(value?.traffic),
    devices: devices !== null && Number.isInteger(devices) ? devices : null,
  };
}
