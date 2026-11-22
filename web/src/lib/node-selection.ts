import { signal } from "@preact/signals";
import { t as tr } from "./i18n";
import { app } from "./app";
import { LatestAction } from "./intent";
import { call } from "./rpc";
import type { Result } from "./types";
import { onAccountChange } from "./account-epoch";
import { errorText } from "./errors";

/** One queue across page navigation: a new selection supersedes older replies. */
export const nodeSelection = signal<{ desired: string | null; pending: boolean; error: string }>({ desired: null, pending: false, error: "" });
onAccountChange(() => { nodeSelection.value = { desired: null, pending: false, error: "" }; });
const selector = new LatestAction<string, Result>(async name => {
  const reply = await call<Result>("select_node", { node: name });
  if (!reply.result) throw new Error(errorText(reply.error, tr("切换失败")));
  return reply;
}, name => {
  nodeSelection.value = { desired: name, pending: false, error: "" };
  if (app.status) app.status = { ...app.status, main_node: name };
  app.tick++;
}, error => {
  nodeSelection.value = { desired: null, pending: false, error: error.name === "AbortError" ? "" : error.message };
});
export function selectNode(name: string): void {
  app.intentRevision++;
  nodeSelection.value = { desired: name, pending: true, error: "" };
  selector.enqueue(name);
}
export function acknowledgeNode(name?: string): void {
  if (!nodeSelection.value.pending && nodeSelection.value.desired === name)
    nodeSelection.value = { desired: null, pending: false, error: "" };
}
