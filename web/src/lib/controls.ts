import { app } from "./app";
import { t as tr } from "./i18n";
import { LatestAction } from "./intent";
import { writePreference } from "./preferences";
import { controlOutcome, operationPending } from "./service";
import type { Mode, Result, ServiceOperation, Status } from "./types";
import { onAccountChange } from "./account-epoch";

let acknowledgedControl: number | null | undefined;
onAccountChange(() => { acknowledgedControl = undefined; });

const enabledWrites = new LatestAction<boolean, Result & { operation?: ServiceOperation }>(
  enabled => writePreference("set_enabled", { enabled }),
  (enabled, result) => {
    acknowledgedControl = result.operation?.id ?? null;
    if (app.status) app.status = { ...app.status, enabled, ...(result.operation ? { operation: result.operation } : {}) };
    app.tick++;
  },
  error => {
    app.controlDesired = null;
    app.controlAction = null;
    app.controlError = error.message;
    acknowledgedControl = undefined;
    app.tick++;
  },
);

const modeWrites = new LatestAction<Mode, Result>(
  mode => writePreference("set_mode", { mode }),
  mode => {
    if (app.status) app.status = { ...app.status, mode };
    app.modeDesired = null;
    app.tick++;
  },
  error => {
    app.modeDesired = null;
    app.modeError = error.message;
    app.tick++;
  },
);

export function desiredEnabled(status: Status): boolean {
  if (app.controlDesired !== null) return app.controlDesired;
  if (operationPending(status.operation)) return status.operation!.action !== "stop";
  return status.running;
}

export function requestEnabled(enabled: boolean): void {
  app.intentRevision++;
  app.controlDesired = enabled;
  app.controlAction = enabled ? "start" : "stop";
  app.controlError = "";
  acknowledgedControl = undefined;
  enabledWrites.enqueue(enabled);
}

export function requestMode(mode: Mode): void {
  if ((app.modeDesired ?? app.status?.mode) === mode) return;
  app.intentRevision++;
  app.modeDesired = mode;
  app.modeError = "";
  modeWrites.enqueue(mode);
}

/** Only a fresh router status confirms that the requested runtime state exists. */
export function reconcileControls(status: Status): void {
  if (app.controlDesired === null || acknowledgedControl === undefined) return;
  const outcome = controlOutcome(status, app.controlDesired, acknowledgedControl);
  if (outcome === "pending") return;
  // A failed operation is told beside the power button, with its reason.
  if (outcome === "failed" && status.operation?.state !== "failed") {
    app.controlError = tr("服务操作未完成，请查看日志");
  }
  app.controlDesired = null;
  app.controlAction = null;
  acknowledgedControl = undefined;
}
