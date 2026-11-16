import type { ServiceOperation } from "./types";

export function operationPending(operation?: ServiceOperation | null): boolean {
  return operation?.state === "queued" || operation?.state === "running";
}

/** Interpolate between authoritative samples; stop advancing after a lost poll. */
export function liveUptime(seconds: number | null | undefined, sampledAt: number, now: number): number | null {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return null;
  return seconds + Math.min(10, Math.max(0, Math.floor((now - sampledAt) / 1000)));
}
