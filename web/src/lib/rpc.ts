/**
 * ubus client for the `luci.hyper` object.
 *
 * The app runs in a same-origin iframe under LuCI (view hyper/app), so it reuses
 * the LuCI session id from the parent window. Outside LuCI (vite dev / preview)
 * it falls back to the mock backend in ./mock.ts.
 */
import { mockCall } from "./mock";

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

/** Call luci.hyper.<method>. Rejects on transport / ubus errors; method-level errors stay in the result. */
export async function call<T = Record<string, unknown>>(method: string, args: Record<string, unknown> = {}): Promise<T> {
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
  if (status !== 0) throw new RpcError(status === 6 ? "没有权限，请重新登录 LuCI" : `ubus status ${status}`, status);
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
