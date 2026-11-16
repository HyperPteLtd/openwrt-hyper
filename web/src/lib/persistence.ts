import type { Status } from "./types";

export type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem"> & Partial<Pick<Storage, "key" | "length">>;
export const DISPLAY_RETENTION = 7 * 86400000;
const INDEX = "hyper:persistent:v2:index";

/** The index also lets logout remove entries that this page never read. */
export function storedKeys(storage: StorageLike | undefined, prefix: string): string[] {
  if (!storage) return [];
  const keys = new Set<string>();
  try {
    const index: unknown = JSON.parse(storage.getItem(INDEX) || "[]");
    if (Array.isArray(index)) for (const key of index) if (typeof key === "string" && key.startsWith(prefix)) keys.add(key);
    if (storage.key) for (let i = 0; i < (storage.length || 0); i++) {
      const key = storage.key(i);
      if (key?.startsWith(prefix)) keys.add(key);
    }
  } catch { /* unavailable storage */ }
  return [...keys];
}

export function writeStored(storage: StorageLike | undefined, key: string, value: unknown): void {
  if (!storage) return;
  try {
    storage.setItem(key, JSON.stringify(value));
    const keys = storedKeys(storage, "hyper:");
    storage.setItem(INDEX, JSON.stringify([...new Set([...keys.filter((k) => k !== INDEX), key])]));
  } catch { /* quota / private mode: the in-memory cache still works */ }
}

export function removeStored(storage: StorageLike | undefined, keys: string[]): void {
  if (!storage || !keys.length) return;
  try {
    for (const key of keys) storage.removeItem(key);
    const removed = new Set(keys);
    const remaining = storedKeys(storage, "hyper:").filter((key) => key !== INDEX && !removed.has(key));
    if (remaining.length) storage.setItem(INDEX, JSON.stringify(remaining));
    else storage.removeItem(INDEX);
  } catch { /* unavailable storage */ }
}

type RecordValue = Record<string, unknown>;
function record(value: unknown): RecordValue {
  return value && typeof value === "object" && !Array.isArray(value) ? value as RecordValue : {};
}

function fields(value: unknown, names: string[]): RecordValue {
  const source = record(value), result: RecordValue = {};
  for (const name of names) {
    const value = source[name];
    if (value === null || typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value))) result[name] = value;
  }
  return result;
}

function localized(value: unknown): unknown {
  if (typeof value === "string") return value;
  return fields(value, ["en", "en-US", "zh", "zh-CN", "zh-TW"]);
}

function list(value: unknown, project: (item: unknown) => RecordValue): RecordValue[] {
  return Array.isArray(value) ? value.slice(0, 1000).map(project) : [];
}

function membership(value: unknown): RecordValue {
  const source = record(value), result = fields(source, ["id", "status", "start_time", "expire_time", "traffic", "upload", "download"]);
  if (source.subscribe === null) result.subscribe = null;
  else if (source.subscribe && typeof source.subscribe === "object") {
    result.subscribe = { ...fields(source.subscribe, ["device_limit", "traffic"]), name: localized(record(source.subscribe).name) };
  }
  return result;
}

function plan(value: unknown): RecordValue {
  const source = record(value);
  const result = fields(source, ["id", "description", "unit_time", "unit_price", "device_limit", "traffic"]);
  result.name = localized(source.name);
  if (Array.isArray(source.discount)) result.discount = list(source.discount, (v) => fields(v, ["quantity", "sell_price", "original_price", "recommend"]));
  return result;
}

/** Explicit display-only projections: never persist arbitrary API response objects. */
export function displayProjection(method: string, path?: unknown): ((value: unknown) => unknown) | undefined {
  if (method === "nodes") return (value) => {
    const source = record(value);
    return { nodes: list(source.nodes, (v) => fields(v, ["name", "label", "country", "city"])), ...fields(source, ["main_node"]) };
  };
  if (method === "lan_clients") return (value) => {
    const source = record(value);
    return {
      clients: list(source.clients, (v) => fields(v, ["mac", "ip", "name", "online"])),
      ...fields(source, ["lan_proxy_mode"]),
      direct_macs: Array.isArray(source.direct_macs) ? source.direct_macs.filter((v) => typeof v === "string").slice(0, 1000) : [],
    };
  };
  if (method !== "api") return undefined;
  let projectData: ((data: RecordValue) => unknown) | undefined;
  switch (path) {
    case "/v1/common/site/config":
      projectData = (data) => ({ currency: fields(data.currency, ["currency_unit", "currency_symbol"]) }); break;
    case "/v1/public/user/info":
      projectData = (data) => fields(data, ["id", "nickname", "commission", "refer_code"]); break;
    case "/v1/public/user/subscribe":
      projectData = (data) => ({ list: list(data.list, membership), ...fields(data, ["total"]) }); break;
    case "/v1/public/user/devices":
      projectData = (data) => ({ list: list(data.list, (v) => fields(v, ["id", "identifier", "ip", "platform", "user_agent", "online", "current", "last_login_at"])), ...fields(data, ["total"]) }); break;
    case "/v1/public/user/affiliate/count":
      projectData = (data) => fields(data, ["registers", "total_commission"]); break;
    case "/v1/public/announcement/list":
      projectData = (data) => ({ announcements: list(data.announcements, (v) => fields(v, ["id", "title", "content", "created_at", "pinned"])), ...fields(data, ["total"]) }); break;
    case "/v1/public/subscribe/list":
      projectData = (data) => ({ list: list(data.list, plan), units: fields(data.units, ["amount_scale", "currency_unit", "currency_symbol"]) }); break;
    case "/v1/public/payment/methods":
      projectData = (data) => ({ list: list(data.list, (v) => ({ ...fields(v, ["id", "platform", "description"]), name: localized(record(v).name) })) }); break;
  }
  if (!projectData) return undefined;
  return (value) => {
    const source = record(value);
    if (source.ok !== true || !source.data || typeof source.data !== "object") return undefined;
    return { ...fields(source, ["ok", "status", "code"]), data: projectData(record(source.data)) };
  };
}

export function statusIdentity(status: Pick<Status, "signed_in" | "email">): string {
  return status.signed_in ? status.email || "signed-in" : "signed-out";
}

function statusDisplay(status: unknown): RecordValue | undefined {
  const source = record(status);
  if (typeof source.signed_in !== "boolean") return undefined;
  return {
    ...fields(source, ["signed_in", "email", "enabled", "main_node", "main_node_label", "mode", "version"]),
    sync: source.sync ? fields(source.sync, ["ok", "time", "count", "added", "removed"]) : null,
  };
}

/** One bootstrap snapshot per router, with account-scoped read caches beneath it. */
export class DisplayPersistence {
  readonly readsPrefix: string;
  private statusKey: string;
  private storage: StorageLike | undefined;
  private now: () => number;
  constructor(storage: StorageLike | undefined, scope: string, now = Date.now) {
    this.storage = storage; this.now = now;
    const encodedScope = encodeURIComponent(scope);
    this.readsPrefix = `hyper:reads:v2:${encodedScope}:`;
    this.statusKey = `hyper:display:v2:${encodedScope}:status`;
  }

  accountPrefix(account: string): string { return `${this.readsPrefix}${encodeURIComponent(account)}:`; }

  readStatus(): Status | null {
    try {
      const saved = JSON.parse(this.storage?.getItem(this.statusKey) || "null");
      if (!saved || !Number.isFinite(saved.time) || saved.time > this.now() || this.now() - saved.time >= DISPLAY_RETENTION) return null;
      const display = statusDisplay(saved.value);
      if (!display) return null;
      // These defaults are placeholders only. Controls wait for the first live status.
      return { ...display, running: false, memory_kb: null, uptime: null, operation: null, api_base: "", health: null, singbox_version: null } as unknown as Status;
    } catch { return null; }
  }

  writeStatus(status: Status): void {
    const value = statusDisplay(status);
    if (!value) return;
    // Status is polled frequently; avoid rewriting flash/browser storage every poll.
    try {
      const previous = JSON.parse(this.storage?.getItem(this.statusKey) || "null");
      if (previous && JSON.stringify(previous.value) === JSON.stringify(value) && this.now() - previous.time < 3600000) return;
    } catch { /* replace malformed snapshots */ }
    writeStored(this.storage, this.statusKey, { value, time: this.now() });
  }

  clear(): void {
    removeStored(this.storage, [this.statusKey, ...storedKeys(this.storage, this.readsPrefix)]);
  }
}
