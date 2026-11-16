import { DISPLAY_RETENTION, DisplayPersistence, displayProjection, removeStored, statusIdentity, storedKeys, writeStored, type StorageLike } from "./persistence.ts";
import type { Status } from "./types";
import { NodeLatencyCache, type NodeMeasurements } from "./node-latency.ts";

export interface CachePolicy {
  ttl: number;
  stale?: number;
  persist?: boolean;
  retention?: number;
  project?: (value: unknown) => unknown;
}
export interface ReadOptions<T> { fresh?: boolean; onUpdate?: (value: T) => void }
interface Entry { value?: unknown; time: number; retry: number; restored?: boolean; pending?: Promise<unknown> }

/** A discarded request must not reach callers after logout or an account change. */
export class CacheInvalidatedError extends Error {
  constructor() { super(""); this.name = "AbortError"; }
}

function successful(value: unknown): boolean {
  const result = value as { ok?: boolean; result?: boolean; error?: string } | null;
  return !!result && result.ok !== false && result.result !== false && !result.error;
}

function displayValue(value: unknown, policy: CachePolicy): string | undefined {
  return JSON.stringify(policy.project ? policy.project(value) : value);
}

/** Bounded reads with explicit display projections for persistent entries. */
export class ReadCache {
  private entries = new Map<string, Entry>();
  private storage?: StorageLike;
  private now: () => number;
  private prefix: string;
  constructor(storage?: StorageLike, now = Date.now, prefix = "hyper:reads:v2:default:") {
    this.storage = storage; this.now = now; this.prefix = prefix;
  }

  invalidate(match: (key: string) => boolean = () => true): void {
    const keys = new Set([...this.entries.keys(), ...storedKeys(this.storage, this.prefix).map((key) => key.slice(this.prefix.length))]);
    const removed: string[] = [];
    for (const key of keys) {
      if (!match(key)) continue;
      this.entries.delete(key); // also detaches older in-flight requests
      removed.push(`${this.prefix}${key}`);
    }
    removeStored(this.storage, removed);
  }

  async read<T>(key: string, load: () => Promise<T>, policy: CachePolicy, opts: ReadOptions<T> = {}): Promise<T> {
    let entry = this.entries.get(key);
    if (!entry) {
      entry = { time: 0, retry: 0 };
      if (policy.persist) {
        try {
          const saved = JSON.parse(this.storage?.getItem(`${this.prefix}${key}`) || "null");
          const retention = policy.retention ?? policy.ttl + (policy.stale || 0);
          if (saved && Number.isFinite(saved.time) && saved.time <= this.now() && this.now() - saved.time < retention) {
            const value = policy.project ? policy.project(saved.value) : saved.value;
            if (successful(value)) {
              entry = { value, time: saved.time, retry: 0, restored: true };
              if (JSON.stringify(value) !== JSON.stringify(saved.value)) writeStored(this.storage, `${this.prefix}${key}`, { value, time: saved.time });
            }
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
    if (!opts.fresh && !current.restored && hasValue && age >= 0 && age < policy.ttl) return structuredClone(current.value) as T;
    const maxStale = current.restored ? policy.retention ?? policy.ttl + (policy.stale || 0) : policy.ttl + (policy.stale || 0);
    const stale = !opts.fresh && hasValue && age >= 0 && age < maxStale;
    if (stale && current.retry > this.now()) return structuredClone(current.value) as T;
    const shown = hasValue ? displayValue(current.value, policy) : undefined;

    if (!current.pending) {
      let succeeded = false;
      current.pending = Promise.resolve().then(load).then((value) => {
        if (this.entries.get(key) !== current) throw new CacheInvalidatedError();
        if (successful(value)) {
          succeeded = true;
          current.value = structuredClone(value);
          current.time = this.now();
          current.restored = false;
          if (policy.persist) {
            const projected = policy.project ? policy.project(value) : value;
            if (projected !== undefined) {
              writeStored(this.storage, `${this.prefix}${key}`, { value: projected, time: current.time });
              const savedKeys = storedKeys(this.storage, this.prefix);
              if (savedKeys.length > 100) removeStored(this.storage, savedKeys.slice(0, savedKeys.length - 100));
            }
          }
        }
        return value;
      }).finally(() => {
        current.pending = undefined;
        current.retry = succeeded ? 0 : this.now() + 10000;
      });
    }
    if (stale) {
      void current.pending.then((value) => {
        if (this.entries.get(key) === current && successful(value) && displayValue(value, policy) !== shown) opts.onUpdate?.(structuredClone(value) as T);
      }).catch(() => { /* keep the last good view; never publish failed refreshes */ });
      return structuredClone(current.value) as T;
    }
    const value = await current.pending;
    if (this.entries.get(key) !== current) throw new CacheInvalidatedError();
    return structuredClone(value) as T;
  }
}

/** Identity changes clear every saved read for this router, including unloaded keys. */
export class AccountReadCache {
  private storage: StorageLike | undefined;
  private now: () => number;
  private display: DisplayPersistence;
  private reads: ReadCache;
  private nodeLatency: NodeLatencyCache;
  private identity: string | undefined;
  generation = 0;

  constructor(storage: StorageLike | undefined, scope: string, now = Date.now) {
    this.storage = storage; this.now = now;
    this.display = new DisplayPersistence(storage, scope, now);
    const cached = this.display.readStatus();
    this.identity = cached ? statusIdentity(cached) : undefined;
    this.reads = this.createReads();
    this.nodeLatency = this.createNodeLatency();
  }

  private createReads(): ReadCache { return new ReadCache(this.storage, this.now, this.display.accountPrefix(this.identity || "unknown")); }
  private createNodeLatency(): NodeLatencyCache { return new NodeLatencyCache(this.storage, `${this.display.accountPrefix(this.identity || "unknown")}latency:measurements`, this.now); }

  setIdentity(account: string): void {
    if (account === this.identity) return;
    this.clear();
    this.identity = account;
    this.reads = this.createReads();
    this.nodeLatency = this.createNodeLatency();
  }

  clear(): void {
    this.generation++;
    this.reads.invalidate();
    this.display.clear();
    this.identity = undefined;
    this.reads = this.createReads();
    this.nodeLatency = this.createNodeLatency();
  }

  readNodeLatency(nodes: readonly { name: string }[]): NodeMeasurements { return this.nodeLatency.read(nodes); }
  beginNodeMeasurement(): (result: Record<string, number | null>) => NodeMeasurements | undefined {
    const generation = this.generation;
    const cache = this.nodeLatency;
    return (result) => generation === this.generation ? cache.remember(result) : undefined;
  }

  readStatus(): Status | null { return this.display.readStatus(); }
  rememberStatus(status: Status): void {
    this.setIdentity(statusIdentity(status));
    this.display.writeStatus(status);
  }
  invalidate(match: (key: string) => boolean = () => true): void { this.reads.invalidate(match); }
  async read<T>(key: string, load: () => Promise<T>, policy: CachePolicy, opts: ReadOptions<T> = {}): Promise<T> {
    const generation = this.generation;
    for (;;) {
      try {
        const value = await this.reads.read(key, load, policy, opts);
        if (generation !== this.generation) throw new CacheInvalidatedError();
        return value;
      } catch (error) {
        // A settings write can invalidate a page's initial read both before and
        // after saving. Retry that read, but never carry it into another account.
        if (!(error instanceof CacheInvalidatedError) || generation !== this.generation) throw error;
      }
    }
  }
}

const MINUTE = 60000;
function persistableRequest(method: string, args: Record<string, unknown>): boolean {
  if (method !== "api") return Object.keys(args).length === 0;
  if (Object.keys(args).some((key) => !["method", "path", "query", "body"].includes(key))) return false;
  if (args.body != null && (typeof args.body !== "object" || Array.isArray(args.body) || Object.keys(args.body).length)) return false;
  if (args.query == null) return true;
  if (typeof args.query !== "object" || Array.isArray(args.query)) return false;
  return Object.entries(args.query).every(([key, value]) => key === "language"
    ? typeof value === "string" && /^[a-z]{2}(?:-[A-Za-z]{2,4})?$/.test(value)
    : ["page", "size"].includes(key) && typeof value === "number" && Number.isSafeInteger(value) && value >= 0);
}

export function readPolicy(method: string, args: Record<string, unknown>): CachePolicy | undefined {
  const persistent = (ttl: number, stale: number): CachePolicy => ({ ttl, stale, persist: persistableRequest(method, args), retention: DISPLAY_RETENTION, project: displayProjection(method, args.path) });
  if (method === "api" && args.method === "GET") {
    switch (args.path) {
      case "/v1/common/site/config":
      case "/v1/public/subscribe/list":
      case "/v1/public/payment/methods": return persistent(10 * MINUTE, 50 * MINUTE);
      case "/v1/public/user/info":
      case "/v1/public/user/subscribe":
      case "/v1/public/user/affiliate/count": return persistent(MINUTE, 4 * MINUTE);
      case "/v1/public/user/devices": return persistent(15000, 45000);
      case "/v1/public/announcement/list": return persistent(5 * MINUTE, 55 * MINUTE);
    }
  }
  switch (method) {
    case "nodes": return persistent(30000, 4 * MINUTE);
    case "get_config": case "rules_list": case "acllist_read": return { ttl: 30000 };
    case "lan_clients": return persistent(5000, 55000);
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
