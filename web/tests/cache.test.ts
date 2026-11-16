import assert from "node:assert/strict";
import { test } from "node:test";
import { CacheInvalidatedError, ReadCache, readPolicy, invalidatedReads } from "../src/lib/cache.ts";

const tick = () => new Promise<void>((r) => setImmediate(r));
const deferred = <T>() => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r; }); return { promise, resolve }; };

test("read cache reuses fresh data, coalesces requests, and clones shared values", async () => {
  let calls = 0;
  const cache = new ReadCache();
  const load = async () => { calls++; return { list: [1] }; };
  const [a, b] = await Promise.all([cache.read("a", load, { ttl: 1000 }), cache.read("a", load, { ttl: 1000 })]);
  a.list.push(2);
  assert.deepEqual(b.list, [1]);
  assert.deepEqual((await cache.read("a", load, { ttl: 1000 })).list, [1]);
  assert.equal(calls, 1);
  await cache.read("a", load, { ttl: 1000 }, { fresh: true });
  assert.equal(calls, 2);
});

test("stale data returns immediately and refreshes its visible consumer", async () => {
  let now = 1000;
  const cache = new ReadCache(undefined, () => now);
  const policy = { ttl: 100, stale: 20000 };
  await cache.read("a", async () => ({ n: 1 }), policy);
  now += 11000;
  const pending = deferred<{ n: number }>();
  let shown = 1;
  assert.deepEqual(await cache.read("a", () => pending.promise, policy, { onUpdate: (v) => { shown = v.n; } }), { n: 1 });
  pending.resolve({ n: 2 });
  await tick();
  assert.equal(shown, 2);
});

test("invalidated in-flight reads cannot repopulate cache or update a new account", async () => {
  const cache = new ReadCache();
  const pending = deferred<{ n: number }>();
  const old = cache.read("a", () => pending.promise, { ttl: 10000 });
  const discarded = assert.rejects(old, CacheInvalidatedError);
  cache.invalidate();
  await cache.read("a", async () => ({ n: 2 }), { ttl: 10000 });
  pending.resolve({ n: 1 });
  await discarded;
  assert.deepEqual(await cache.read("a", async () => ({ n: 3 }), { ttl: 10000 }), { n: 2 });
});

test("errors are never cached; expired values do not mask a failed fresh request", async () => {
  let calls = 0;
  const cache = new ReadCache();
  const load = async () => { calls++; return { ok: false, error: "offline" }; };
  await cache.read("a", load, { ttl: 10000 });
  await cache.read("a", load, { ttl: 10000 });
  assert.equal(calls, 2);
  await assert.rejects(cache.read("b", async () => { throw Error("offline"); }, { ttl: 1000 }));
});

test("restored display data returns immediately and revalidates even within its TTL", async () => {
  const values = new Map<string, string>();
  const storage = { getItem: (k: string) => values.get(k) ?? null, setItem: (k: string, v: string) => { values.set(k, v); }, removeItem: (k: string) => { values.delete(k); } };
  let now = 1000;
  const cache = new ReadCache(storage, () => now);
  await cache.read("public", async () => ({ name: "Basic" }), { ttl: 100, persist: true });
  await cache.read("private", async () => ({ email: "private@example.com" }), { ttl: 100 });
  assert(![...values.keys()].some((key) => key.includes("private")));
  const restored = new ReadCache(storage, () => now);
  const pending = deferred<{ name: string }>();
  let calls = 0, shown = "Basic";
  const load = () => { calls++; return pending.promise; };
  assert.deepEqual(await restored.read("public", load, { ttl: 100, persist: true }, { onUpdate: (v) => { shown = v.name; } }), { name: "Basic" });
  assert.equal(calls, 1);
  pending.resolve({ name: "New" });
  await tick();
  assert.equal(shown, "New");
  now += 101;
  assert.deepEqual(await new ReadCache(storage, () => now).read("public", async () => ({ name: "Expired" }), { ttl: 100, persist: true }), { name: "Expired" });
});

test("unchanged display values and failed refreshes do not notify or replace the last good value", async () => {
  let now = 1000, updates = 0;
  const cache = new ReadCache(undefined, () => now);
  const policy = { ttl: 100, stale: 60000 };
  await cache.read("a", async () => ({ n: 1 }), policy);
  now += 1000;
  await cache.read("a", async () => ({ n: 1 }), policy, { onUpdate: () => updates++ });
  await tick();
  now += 1000;
  await cache.read("a", async () => ({ ok: false, error: "offline" }), policy, { onUpdate: () => updates++ });
  await tick();
  assert.equal(updates, 0);
  assert.deepEqual(await cache.read("a", async () => ({ n: 2 }), policy), { n: 1 });
});

test("invalidation removes persisted entries that have not been loaded into memory", async () => {
  const values = new Map<string, string>();
  const storage = { getItem: (k: string) => values.get(k) ?? null, setItem: (k: string, v: string) => { values.set(k, v); }, removeItem: (k: string) => { values.delete(k); } };
  const first = new ReadCache(storage);
  await first.read("api:account", async () => ({ id: 1 }), { ttl: 1000, persist: true });
  await first.read("nodes:list", async () => ({ count: 2 }), { ttl: 1000, persist: true });
  const reloaded = new ReadCache(storage);
  reloaded.invalidate((key) => key.startsWith("api:"));
  assert(![...values.keys()].some((key) => key.endsWith("api:account")));
  assert([...values.keys()].some((key) => key.endsWith("nodes:list")));
  reloaded.invalidate();
  assert.equal(values.size, 0);
});

test("live service and payment results bypass cache; writes invalidate affected reads", () => {
  for (const method of ["status", "traffic", "health_check", "diag", "logs", "latency"]) assert.equal(readPolicy(method, {}), undefined);
  assert.equal(readPolicy("api", { method: "GET", path: "/v2/orders/123/payment" }), undefined);
  assert.equal(readPolicy("api", { method: "POST", path: "/v1/public/order/pre" }), undefined);
  assert.deepEqual(invalidatedReads("logout", {}), [""]);
  assert(invalidatedReads("sync_subscription", {}).includes("api:"));
  assert(invalidatedReads("set_config", {}).includes("get_config:"));
  assert.equal(readPolicy("api", { method: "GET", path: "/v1/public/user/subscribe", query: { token: "must-not-persist" } })?.persist, false);
  assert.equal(readPolicy("api", { method: "GET", path: "/v1/public/user/info", body: { jwt: "must-not-persist" } })?.persist, false);
});
