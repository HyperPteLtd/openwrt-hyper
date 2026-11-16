import { t as tr } from "./i18n";
/**
 * ubus client for the `luci.hyper` object.
 *
 * The app runs in a same-origin iframe under LuCI (view hyper/app), so it reuses
 * the LuCI session id from the parent window. Outside LuCI (vite dev / preview)
 * it falls back to the mock backend in ./mock.ts.
 */
import { mockCall } from "./mock";
import { AccountReadCache, CacheInvalidatedError, invalidatedReads, readPolicy, type ReadOptions } from "./cache";
import type { Status } from "./types";
import { waitForRpcTask, type RpcTaskState } from "./rpc-task";
import { invalidateAccountWork } from "./account-epoch";

type LuciEnv = { sessionid?: string; ubuspath?: string };

function luciEnv(): LuciEnv | null {
  try {
    const parent = window.parent as unknown as { L?: { env?: LuciEnv } };
    if (parent && parent !== window && parent.L?.env?.sessionid) return parent.L.env;
  } catch {
    /* cross-origin parent: not inside LuCI */
  }
  return null;
}

export const isMock = (): boolean => luciEnv() === null;

export class RpcError extends Error {
  constructor(
    message: string,
    public code?: number,
  ) {
    super(message);
  }
}

let seq = 0;
let storage: Storage | undefined;
try { storage = localStorage; } catch { /* storage disabled */ }
const scope = `${isMock() ? "demo" : "router"}:${window.location.origin}`;
const reads = new AccountReadCache(storage, scope);

export function setCacheIdentity(account: string): void {
  const generation = reads.generation;
  reads.setIdentity(account);
  if (generation !== reads.generation) invalidateAccountWork();
}

/** Display-only bootstrap data. Service controls still wait for a live status. */
export function readCachedStatus(): Status | null { return reads.readStatus(); }
export function rememberStatus(status: Status): void {
  const generation = reads.generation;
  reads.rememberStatus(status);
  if (generation !== reads.generation) invalidateAccountWork();
}
export function readNodeLatency(nodes: readonly { name: string }[]) { return reads.readNodeLatency(nodes); }
export function beginNodeMeasurement() { return reads.beginNodeMeasurement(); }

/** Cache only named read methods; live status, diagnostics and payments bypass it. */
export async function call<T = Record<string, unknown>>(method: string, args: Record<string, unknown> = {}, opts: ReadOptions<T> = {}): Promise<T> {
  if (["login", "register", "logout"].includes(method)) {
    reads.clear();
    invalidateAccountWork();
    try { return await send<T>(method, args); } finally {
      reads.clear();
      invalidateAccountWork();
    }
  }
  const generation = reads.generation;
  const prefixes = invalidatedReads(method, args);
  const invalidate = () => {
    if (prefixes.length && generation === reads.generation) reads.invalidate((key) => prefixes.some((prefix) => key.startsWith(prefix)));
  };
  const policy = readPolicy(method, args);
  if (policy) return reads.read(`${method}:${JSON.stringify(args)}`, () => send<T>(method, args), policy, opts);
  invalidate();
  try {
    const result = await send<T>(method, args);
    if (generation !== reads.generation) throw new CacheInvalidatedError();
    return result;
  } finally { invalidate(); }
}

/** Call luci.hyper.<method>. Rejects on transport / ubus errors; method-level errors stay in the result. */
async function send<T>(method: string, args: Record<string, unknown>): Promise<T> {
  const generation = reads.generation;
  const result = await sendOnce<T & { _hyper_task?: string; _hyper_timeout?: number }>(method, args);
  if (!result?._hyper_task) return result;
  try {
    return await waitForRpcTask(result._hyper_timeout,
      () => sendOnce<RpcTaskState<T>>("task_status", { id: result._hyper_task }),
      () => { if (generation !== reads.generation) throw new CacheInvalidatedError(); });
  } catch (error) {
    if (error instanceof CacheInvalidatedError || error instanceof RpcError) throw error;
    throw new RpcError((error as Error).message);
  }
}

async function sendOnce<T>(method: string, args: Record<string, unknown>): Promise<T> {
  const env = luciEnv();
  if (!env) return mockCall(method, args) as Promise<T>;

  const res = await fetch(env.ubuspath || "/ubus/", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++seq,
      method: "call",
      params: [env.sessionid, "luci.hyper", method, args],
    }),
  });
  if (!res.ok) throw new RpcError(`HTTP ${res.status}`);

  const msg = await res.json();
  if (msg.error) throw new RpcError(msg.error.message || "ubus error", msg.error.code);

  /* ubus replies [status, data]; status 0 = OK, 6 = permission denied */
  const [status, data] = msg.result as [number, T];
  if (status !== 0) throw new RpcError(status === 6 ? tr("没有权限，请重新登录 LuCI") : `ubus status ${status}`, status);
  return data;
}

/** Keep the LuCI iframe as tall as the app so LuCI's page scrolls, not the frame. */
export function reportHeight(): void {
  try {
    window.parent.postMessage({ type: "hyper:height", height: document.documentElement.scrollHeight }, window.location.origin);
  } catch {
    /* ignore */
  }
}
