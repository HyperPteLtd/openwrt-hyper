import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { t as tr } from "../lib/i18n";
import { configWrites, deviceWrites, listWrites, ruleWrites } from "../lib/preferences";
import "./AutosaveStatus.css";

interface WriteQueue {
  busy: boolean;
  error: Error | null;
  savedRevision: number;
  subscribe(listener: () => void): () => void;
  flush(): Promise<void>;
  retry(): Promise<void>;
}

const preferenceQueues = [configWrites, ruleWrites, deviceWrites, ...Object.values(listWrites)];

/** Subscribe to global queues and flush valid edits when leaving a page. */
export function useAutosave(...queues: WriteQueue[]): void {
  const revision = useSignal(0);
  void revision.value;
  useEffect(() => {
    const cleanup = queues.map((queue) => queue.subscribe(() => { revision.value++; }));
    return () => {
      cleanup.forEach((remove) => remove());
      queues.forEach((queue) => { void queue.flush().catch(() => { /* the global status keeps failures visible */ }); });
    };
  }, queues);
}

export function AutosaveStatus({ queues = preferenceQueues }: { queues?: WriteQueue[] }) {
  useAutosave(...queues);
  const failed = queues.filter((queue) => queue.error);
  const state = failed.length ? "failed" : queues.some((queue) => queue.busy) ? "pending" : queues.some((queue) => queue.savedRevision > 0) ? "saved" : "idle";
  const label = state === "failed" ? tr("保存失败") : state === "pending" ? tr("保存中…") : state === "saved" ? tr("已保存") : tr("修改后自动保存并应用");
  return (
    <div class="autosave-status" data-state={state} role="status" aria-live="polite" aria-atomic="true">
      {state === "pending" && <span class="autosave-spinner" aria-hidden="true" />}
      {state === "saved" && <svg class="autosave-check" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3 8 3 3 7-7" /></svg>}
      <span>{label}</span>
      {!!failed.length && <><span aria-hidden="true">·</span><button type="button" class="autosave-retry" onClick={() => { void Promise.allSettled(failed.map((queue) => queue.retry())); }}>{tr("重试")}</button></>}
    </div>
  );
}
