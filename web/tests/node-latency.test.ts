import assert from "node:assert/strict";
import { test } from "node:test";
import { AccountReadCache, readPolicy } from "../src/lib/cache.ts";
import { NodeLatencyCache } from "../src/lib/node-latency.ts";
import { DISPLAY_RETENTION, DisplayPersistence, storedKeys, writeStored } from "../src/lib/persistence.ts";
import type { Status } from "../src/lib/types.ts";

function memoryStorage() {
  const values = new Map<string, string>();
  return {
    values,
    get length() { return values.size; },
    key: (index: number) => [...values.keys()][index] ?? null,
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value); },
    removeItem: (key: string) => { values.delete(key); },
  };
}
const status = (email = "alice@example.com") => ({ signed_in: true, email }) as Status;
const nodes = [{ name: "pp_first", label: "JPN 01" }, { name: "pp_second", label: "USA 01" }];

test("measurement snapshots survive navigation/reload, while explicit measurements stay uncached and replace success", () => {
  const storage = memoryStorage();
  let now = 1000;
  const cache = new AccountReadCache(storage, "router:one", () => now);
  cache.rememberStatus(status());
  assert.deepEqual(cache.readNodeLatency(nodes), {});
  cache.beginNodeMeasurement()({ pp_first: 36, pp_second: 128 });
  assert.deepEqual(cache.readNodeLatency(nodes), { pp_first: { ms: 36, measuredAt: 1000 }, pp_second: { ms: 128, measuredAt: 1000 } });
  now += 100;
  const reloaded = new AccountReadCache(storage, "router:one", () => now);
  reloaded.setIdentity("alice@example.com");
  assert.equal(reloaded.readNodeLatency(nodes).pp_first.ms, 36);
  reloaded.beginNodeMeasurement()({ pp_first: 24 });
  assert.deepEqual(reloaded.readNodeLatency(nodes).pp_first, { ms: 24, measuredAt: 1100 });
  assert.equal(reloaded.readNodeLatency(nodes).pp_second.ms, 128);
  assert.equal(readPolicy("latency", {}), undefined, "each button action still reaches the router");
});

test("failed measurements retain the last successful time/value; new-node failures are recorded without losing other nodes", () => {
  let now = 1000;
  const cache = new AccountReadCache(undefined, "router:one", () => now);
  cache.readNodeLatency(nodes);
  cache.beginNodeMeasurement()({ pp_first: 36 });
  now += 100;
  cache.beginNodeMeasurement()({ pp_first: null, pp_second: null, unknown: 1 });
  assert.deepEqual(cache.readNodeLatency(nodes), { pp_first: { ms: 36, measuredAt: 1000 }, pp_second: { ms: null, measuredAt: 1100 } });
  cache.beginNodeMeasurement()({ pp_first: -1, pp_second: 60 });
  assert.deepEqual(cache.readNodeLatency(nodes), { pp_first: { ms: 36, measuredAt: 1000 }, pp_second: { ms: 60, measuredAt: 1100 } });
});

test("node removal deletes its snapshot and late measurements cannot reintroduce a removed/replaced node", () => {
  const storage = memoryStorage();
  const cache = new AccountReadCache(storage, "router:one");
  cache.rememberStatus(status());
  cache.readNodeLatency(nodes);
  cache.beginNodeMeasurement()({ pp_first: 36, pp_second: 128 });
  const finish = cache.beginNodeMeasurement();
  const replaced = [{ name: "pp_replaced", label: "JPN 01" }, nodes[1]];
  assert.deepEqual(Object.keys(cache.readNodeLatency(replaced)), ["pp_second"]);
  finish({ pp_first: 20, pp_second: 130 });
  assert.deepEqual(Object.keys(cache.readNodeLatency(replaced)), ["pp_second"]);
  assert(!JSON.stringify([...storage.values]).includes("pp_first"));
});

test("router/account scopes isolate measurements; logout clears unloaded snapshots and discards pending saves", () => {
  const storage = memoryStorage();
  const first = new AccountReadCache(storage, "router:one");
  first.rememberStatus(status()); first.readNodeLatency(nodes);
  first.beginNodeMeasurement()({ pp_first: 36 });
  const second = new AccountReadCache(storage, "router:two");
  second.rememberStatus(status());
  assert.deepEqual(second.readNodeLatency(nodes), {});
  second.beginNodeMeasurement()({ pp_first: 99 });
  const display = new DisplayPersistence(storage, "router:one");
  writeStored(storage, `${display.accountPrefix("old-account")}latency:measurements`, { pp_first: { ms: 9, measuredAt: Date.now() } });
  const reloaded = new AccountReadCache(storage, "router:one");
  const finish = reloaded.beginNodeMeasurement();
  reloaded.clear();
  assert.deepEqual(storedKeys(storage, display.readsPrefix), []);
  reloaded.rememberStatus(status("bob@example.com"));
  assert.equal(finish({ pp_first: 15 }), undefined);
  assert.deepEqual(reloaded.readNodeLatency(nodes), {});
  assert.equal(second.readNodeLatency(nodes).pp_first.ms, 99);
  reloaded.beginNodeMeasurement()({ pp_first: 11 });
  reloaded.setIdentity("carol@example.com");
  assert.deepEqual(reloaded.readNodeLatency(nodes), {});
});

test("restore strips arbitrary fields and invalid/future/expired results, and storage failure keeps in-memory results", () => {
  const storage = memoryStorage();
  const now = DISPLAY_RETENTION + 1000;
  const key = "hyper:reads:v2:test:latency:measurements";
  writeStored(storage, key, {
    pp_first: { ms: 36, measuredAt: now - 100, token: "secret" },
    pp_expired: { ms: 20, measuredAt: 1000 },
    pp_future: { ms: 20, measuredAt: now + 1 },
    pp_bad: { ms: "bad", measuredAt: now },
  });
  assert.deepEqual(new NodeLatencyCache(storage, key, () => now).read(nodes), { pp_first: { ms: 36, measuredAt: now - 100 } });
  assert(!JSON.stringify([...storage.values]).includes("secret"));
  const denied = () => { throw Error("storage disabled"); };
  const cache = new NodeLatencyCache({ getItem: denied, setItem: denied, removeItem: denied }, key);
  cache.read(nodes); cache.remember({ pp_first: 36 });
  assert.equal(cache.read(nodes).pp_first.ms, 36);
});
