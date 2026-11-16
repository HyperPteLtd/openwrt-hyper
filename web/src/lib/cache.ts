export interface CachePolicy { ttl: number; stale?: number; persist?: boolean }
export interface ReadOptions<T> { fresh?: boolean; onUpdate?: (value: T) => void }
type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">;
interface Entry { value?: unknown; time: number; retry: number; pending?: Promise<unknown> }

/** Bounded read cache. Only explicitly public data may survive a page reload. */
export class ReadCache {
  private entries = new Map<string, Entry>();
  private storage?: StorageLike;
  private now: () => number;
  constructor(storage?: StorageLike, now = Date.now) { this.storage = storage; this.now = now; }

  invalidate(match: (key: string) => boolean = () => true): void {
    for (const key of this.entries.keys()) {
      if (!match(key)) continue;
      this.entries.delete(key); // also detaches any older in-flight request
      try { this.storage?.removeItem(`hyper:reads:v1:${key}`); } catch { /* storage disabled */ }
    }
  }

  async read<T>(key: string, load: () => Promise<T>, policy: CachePolicy, opts: ReadOptions<T> = {}): Promise<T> {
    let entry = this.entries.get(key);
    if (!entry) {
      entry = { time: 0, retry: 0 };
      if (policy.persist) {
        try {
          const saved = JSON.parse(this.storage?.getItem(`hyper:reads:v1:${key}`) || "null");
          if (saved && Number.isFinite(saved.time) && saved.time <= this.now() && this.now() - saved.time < policy.ttl + (policy.stale || 0)) {
            entry = { value: saved.value, time: saved.time, retry: 0 };
          }
        } catch { /* malformed or unavailable storage: fetch normally */ }
      }
      if (this.entries.size >= 100) {
        const oldest = this.entries.keys().next().value;
        this.invalidate((k) => k === oldest);
      }
      this.entries.set(key, entry);
    }
    const current = entry;
    const age = this.now() - current.time;
    const hasValue = current.value !== undefined;
    if (!opts.fresh && hasValue && age >= 0 && age < policy.ttl) return structuredClone(current.value) as T;
    const stale = !opts.fresh && hasValue && age >= 0 && age < policy.ttl + (policy.stale || 0);
    if (stale && current.retry > this.now()) return structuredClone(current.value) as T;

    if (!current.pending) {
      current.pending = Promise.resolve().then(load).then((value) => {
        const result = value as { ok?: boolean; result?: boolean; error?: string } | null;
        if (this.entries.get(key) === current && result && result.ok !== false && result.result !== false && !result.error) {
          current.value = structuredClone(value);
          current.time = this.now();
          if (policy.persist) {
            try { this.storage?.setItem(`hyper:reads:v1:${key}`, JSON.stringify({ value: current.value, time: current.time })); } catch { /* quota / private mode */ }
          }
        }
        return value;
      }).finally(() => {
        current.pending = undefined;
        current.retry = this.now() + 10000;
      });
    }
    if (stale) {
      void current.pending.then(() => {
        if (this.entries.get(key) === current && current.time > this.now() - policy.ttl) opts.onUpdate?.(structuredClone(current.value) as T);
      }).catch(() => { /* keep the last good view; do not cache a failed response */ });
      return structuredClone(current.value) as T;
    }
    return structuredClone(await current.pending) as T;
  }
}

const MINUTE = 60000;
export function readPolicy(method: string, args: Record<string, unknown>): CachePolicy | undefined {
  if (method === "api" && args.method === "GET") {
    switch (args.path) {
      case "/v1/public/subscribe/list":
      case "/v1/public/payment/methods": return { ttl: 10 * MINUTE, stale: 50 * MINUTE, persist: true };
      case "/v1/public/user/info":
      case "/v1/public/user/subscribe":
      case "/v1/public/user/affiliate/count": return { ttl: MINUTE, stale: 4 * MINUTE };
      case "/v1/public/user/devices": return { ttl: 15000, stale: 45000 };
      case "/v1/public/announcement/list": return { ttl: 5 * MINUTE, stale: 55 * MINUTE };
    }
  }
  switch (method) {
    case "nodes": return { ttl: 30000, stale: 4 * MINUTE };
    case "get_config": case "rules_list": case "acllist_read": return { ttl: 30000 };
    case "lan_clients": return { ttl: 5000 };
    case "checkin_status": return { ttl: MINUTE };
    case "check_update": return { ttl: 10 * MINUTE };
  }
}

export function invalidatedReads(method: string, args: Record<string, unknown>): string[] {
  if (["login", "register", "logout"].includes(method)) return [""];
  if (method === "api" && args.method !== "GET" && args.path !== "/v1/public/order/pre") return ["api:"];
  if (method === "sync_subscription") return ["nodes:", "rules_list:", "api:"];
  if (["set_config", "set_mode", "set_enabled", "set_rule_enabled", "acllist_write", "set_access_control", "select_node"].includes(method)) {
    return ["get_config:", "rules_list:", "acllist_read:", "lan_clients:", "nodes:"];
  }
  if (method === "checkin") return ["checkin_status:"];
  if (method === "install_update") return ["check_update:"];
  return [];
}
