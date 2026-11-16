import assert from "node:assert/strict";
import { test } from "node:test";
import { ReadCache, readPolicy, invalidatedReads } from "../src/lib/cache.ts";

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
  cache.invalidate();
  await cache.read("a", async () => ({ n: 2 }), { ttl: 10000 });
  pending.resolve({ n: 1 });
  await old;
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

test("only public catalog data persists, and a reload obeys its expiry", async () => {
  const values = new Map<string, string>();
  const storage = { getItem: (k: string) => values.get(k) ?? null, setItem: (k: string, v: string) => { values.set(k, v); }, removeItem: (k: string) => { values.delete(k); } };
  let now = 1000;
  const cache = new ReadCache(storage, () => now);
  await cache.read("public", async () => ({ name: "Basic" }), { ttl: 100, persist: true });
  await cache.read("private", async () => ({ email: "private@example.com" }), { ttl: 100 });
  assert.equal(values.size, 1);
  const restored = new ReadCache(storage, () => now);
  assert.deepEqual(await restored.read("public", async () => ({ name: "New" }), { ttl: 100, persist: true }), { name: "Basic" });
  now += 101;
  assert.deepEqual(await restored.read("public", async () => ({ name: "New" }), { ttl: 100, persist: true }), { name: "New" });
});

test("live service and payment results bypass cache; writes invalidate affected reads", () => {
  for (const method of ["status", "traffic", "health_check", "diag", "logs", "latency"]) assert.equal(readPolicy(method, {}), undefined);
  assert.equal(readPolicy("api", { method: "GET", path: "/v2/orders/123/payment" }), undefined);
  assert.equal(readPolicy("api", { method: "POST", path: "/v1/public/order/pre" }), undefined);
  assert.deepEqual(invalidatedReads("logout", {}), [""]);
  assert(invalidatedReads("sync_subscription", {}).includes("api:"));
  assert(invalidatedReads("set_config", {}).includes("get_config:"));
});
