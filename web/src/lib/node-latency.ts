import { DISPLAY_RETENTION, removeStored, writeStored, type StorageLike } from "./persistence.ts";

export interface NodeMeasurement { ms: number | null; measuredAt: number }
export type NodeMeasurements = Record<string, NodeMeasurement>;

/** Display snapshots only; the latency RPC must always perform a new measurement. */
export class NodeLatencyCache {
  private values: NodeMeasurements | undefined;
  private names: Set<string> | undefined;
  private storage: StorageLike | undefined;
  private key: string;
  private now: () => number;
  constructor(storage: StorageLike | undefined, key: string, now = Date.now) {
    this.storage = storage; this.key = key; this.now = now;
  }

  private restore(): NodeMeasurements {
    if (this.values) return this.values;
    const values: NodeMeasurements = {};
    try {
      const saved = JSON.parse(this.storage?.getItem(this.key) || "null");
      if (saved && typeof saved === "object" && !Array.isArray(saved)) {
        for (const [name, value] of Object.entries(saved).slice(0, 1000)) {
          const item = value as NodeMeasurement | null;
          if (!/^[A-Za-z0-9_]+$/.test(name) || !item || !Number.isFinite(item.measuredAt) || item.measuredAt > this.now() || this.now() - item.measuredAt >= DISPLAY_RETENTION) continue;
          if (item.ms === null || (typeof item.ms === "number" && Number.isFinite(item.ms) && item.ms > 0)) values[name] = { ms: item.ms, measuredAt: item.measuredAt };
        }
      }
    } catch { /* storage disabled or malformed */ }
    this.values = values;
    this.persist();
    return values;
  }

  private persist(): void {
    if (Object.keys(this.values || {}).length) writeStored(this.storage, this.key, this.values);
    else removeStored(this.storage, [this.key]);
  }

  read(nodes: readonly { name: string }[]): NodeMeasurements {
    const values = this.restore();
    // Subscription node names hash the server identity, so replaced endpoints do
    // not inherit a previous server's result even when their labels are equal.
    this.names = new Set(nodes.slice(0, 1000).map((node) => node.name));
    let changed = false;
    for (const name of Object.keys(values)) {
      if (!this.names.has(name) || this.now() - values[name].measuredAt >= DISPLAY_RETENTION) { delete values[name]; changed = true; }
    }
    if (changed) this.persist();
    return structuredClone(values);
  }

  remember(result: Record<string, number | null>): NodeMeasurements {
    const values = this.restore();
    let changed = false;
    for (const [name, ms] of Object.entries(result)) {
      // Ignore unknown/removed nodes, including late replies after a list refresh.
      if (!this.names?.has(name)) continue;
      const valid = typeof ms === "number" && Number.isFinite(ms) && ms > 0;
      // A failed attempt must not erase the last successful measurement or its time.
      if (!valid && values[name]?.ms != null) continue;
      values[name] = { ms: valid ? ms : null, measuredAt: this.now() };
      changed = true;
    }
    if (changed) this.persist();
    return structuredClone(values);
  }
}
