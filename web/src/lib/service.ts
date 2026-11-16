import type { ServiceOperation, Status } from "./types";

export function operationPending(operation?: ServiceOperation | null): boolean {
  return operation?.state === "queued" || operation?.state === "running";
}

/** A later restart can supersede Start; its completed state also settles the intent. */
export function controlOutcome(status: Pick<Status, "running" | "operation">, desired: boolean, acknowledged: number | null | undefined): "pending" | "done" | "failed" {
  if (acknowledged === undefined) return "pending";
  const operation = status.operation;
  if (operation) {
    if (acknowledged !== null && operation.id < acknowledged) return "pending";
    if (operationPending(operation)) return "pending";
    if (operation.state === "failed" || status.running !== desired) return "failed";
  }
  return status.running === desired ? "done" : "pending";
}

/** Interpolate between authoritative samples; stop advancing after a lost poll. */
export function liveUptime(seconds: number | null | undefined, sampledAt: number, now: number): number | null {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return null;
  return seconds + Math.min(10, Math.max(0, Math.floor((now - sampledAt) / 1000)));
}
