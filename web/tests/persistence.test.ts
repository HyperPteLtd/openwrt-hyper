import assert from "node:assert/strict";
import { test } from "node:test";
import { AccountReadCache, CacheInvalidatedError, readPolicy } from "../src/lib/cache.ts";
import { DISPLAY_RETENTION, DisplayPersistence, displayProjection, storedKeys, writeStored } from "../src/lib/persistence.ts";
import type { Status } from "../src/lib/types.ts";

const tick = () => new Promise<void>((resolve) => setImmediate(resolve));
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
const status = (email = "alice@example.com"): Status => ({
  enabled: true, running: true, uptime: 1234, memory_kb: 50000,
  main_node: "jpn-01", main_node_label: "JPN 01", mode: "smart",
  signed_in: true, email, api_base: "https://private-api.example.com", version: "0.2.1-r1", singbox_version: "private-engine",
  sync: { ok: true, time: 123, count: 6 }, health: { state: "ok", failures: 0, time: 123 },
  operation: { id: 1, action: "start", state: "running", time: 123 },
});

test("status bootstrap persists only identity/configuration display fields, never runtime state or credentials", () => {
  const storage = memoryStorage();
  const display = new DisplayPersistence(storage, "router:https://router.test");
  display.writeStatus({ ...status(), jwt: "private-jwt", subscription_url: "https://private-subscription", sync: { ...status().sync!, token: "private-token", traffic_used: "1 TB" } } as Status);
  const saved = JSON.parse(storage.getItem(storedKeys(storage, "hyper:display:")[0])!);
  for (const key of ["running", "uptime", "memory_kb", "operation", "health", "api_base", "singbox_version", "jwt", "subscription_url"]) assert(!(key in saved.value), key);
  assert.deepEqual(saved.value.sync, { ok: true, time: 123, count: 6 });
  assert.equal(display.readStatus()?.email, "alice@example.com");
  assert.equal(display.readStatus()?.uptime, null);
  assert.equal(display.readStatus()?.health, null);
  assert(!JSON.stringify([...storage.values]).includes("private-"));
});

test("reload retains the same identity cache and refreshes the restored value in the background", async () => {
  const storage = memoryStorage();
  const cache = new AccountReadCache(storage, "router:https://router.test");
  cache.rememberStatus(status());
  const args = { method: "GET", path: "/v1/public/user/info" };
  const policy = readPolicy("api", args)!;
  await cache.read("api:user", async () => ({ ok: true, data: { id: 1, nickname: "Alice", jwt: "secret" } }), policy);
  const restored = new AccountReadCache(storage, "router:https://router.test");
  restored.setIdentity("alice@example.com");
  let updates = 0;
  const value = await restored.read("api:user", async () => ({ ok: true, data: { id: 1, nickname: "Alice", jwt: "different-secret" } }), policy, { onUpdate: () => updates++ });
  assert.deepEqual(value, { ok: true, data: { id: 1, nickname: "Alice" } });
  await tick();
  assert.equal(updates, 0, "credential-only changes do not redraw display data");
  assert(!JSON.stringify([...storage.values]).includes("secret"));
});

test("router and account scopes stay isolated; logout clears unloaded entries for every old account", async () => {
  const storage = memoryStorage();
  const first = new AccountReadCache(storage, "router:https://one.test");
  first.rememberStatus(status());
  const policy = readPolicy("nodes", {})!;
  await first.read("nodes:{}", async () => ({ nodes: [{ name: "a", label: "JPN 01" }], main_node: "a" }), policy);
  const second = new AccountReadCache(storage, "router:https://two.test");
  second.rememberStatus(status("bob@example.com"));
  await second.read("nodes:{}", async () => ({ nodes: [{ name: "b", label: "USA 01" }], main_node: "b" }), policy);
  const firstDisplay = new DisplayPersistence(storage, "router:https://one.test");
  writeStored(storage, `${firstDisplay.accountPrefix("forgotten-account")}api:unloaded`, { value: { nickname: "old account" }, time: Date.now() });
  const reloaded = new AccountReadCache(storage, "router:https://one.test");
  reloaded.clear();
  assert.equal(reloaded.readStatus(), null);
  assert.deepEqual(storedKeys(storage, firstDisplay.readsPrefix), []);
  assert.equal(second.readStatus()?.email, "bob@example.com");
  assert(storedKeys(storage, new DisplayPersistence(storage, "router:https://two.test").readsPrefix).length > 0);
});

test("switching identity discards pending results and cannot repersist data from the old account", async () => {
  const storage = memoryStorage();
  const cache = new AccountReadCache(storage, "router:https://router.test");
  cache.rememberStatus(status());
  const policy = readPolicy("nodes", {})!;
  let resolve!: (value: unknown) => void;
  const pending = cache.read("nodes:{}", () => new Promise((r) => { resolve = r; }), policy);
  const discarded = assert.rejects(pending, CacheInvalidatedError);
  await tick();
  cache.rememberStatus(status("bob@example.com"));
  await cache.read("nodes:{}", async () => ({ nodes: [{ name: "bob", label: "USA 01" }], main_node: "bob" }), policy);
  resolve({ nodes: [{ name: "alice-secret", label: "Old" }], main_node: "alice-secret" });
  await discarded;
  assert.equal(cache.readStatus()?.email, "bob@example.com");
  assert(!JSON.stringify([...storage.values]).includes("alice"));
});

test("a page's initial reads retry and coalesce when a settings save invalidates them twice", async () => {
  const cache = new AccountReadCache(undefined, "router:https://router.test");
  cache.setIdentity("alice@example.com");
  const pending: ((value: { revision: number }) => void)[] = [];
  const load = () => new Promise<{ revision: number }>((resolve) => { pending.push(resolve); });
  const policy = readPolicy("get_config", {})!;
  const first = cache.read("get_config:{}", load, policy);
  const second = cache.read("get_config:{}", load, policy);
  await tick();
  assert.equal(pending.length, 1);
  cache.invalidate((key) => key.startsWith("get_config:")); // save starts
  pending[0]({ revision: 0 });
  await tick();
  assert.equal(pending.length, 2);
  cache.invalidate((key) => key.startsWith("get_config:")); // save completes
  pending[1]({ revision: 1 });
  await tick();
  assert.equal(pending.length, 3);
  pending[2]({ revision: 2 });
  assert.deepEqual(await Promise.all([first, second]), [{ revision: 2 }, { revision: 2 }]);
  assert.equal(pending.length, 3, "consumers share every retry instead of multiplying requests");
});

test("logout during a mutation retry rejects the old read without starting a request for the new identity", async () => {
  const cache = new AccountReadCache(undefined, "router:https://router.test");
  cache.setIdentity("alice@example.com");
  const pending: ((value: { account: string }) => void)[] = [];
  const read = cache.read("rules_list:{}", () => new Promise<{ account: string }>((resolve) => { pending.push(resolve); }), readPolicy("rules_list", {})!);
  const discarded = assert.rejects(read, CacheInvalidatedError);
  await tick();
  cache.invalidate();
  pending[0]({ account: "alice" });
  await tick();
  assert.equal(pending.length, 2);
  cache.clear();
  cache.setIdentity("bob@example.com");
  pending[1]({ account: "alice" });
  await discarded;
  assert.equal(pending.length, 2);
});

test("a real network failure during a retried initial read still reaches its caller", async () => {
  const cache = new AccountReadCache(undefined, "router:https://router.test");
  let complete!: (value: object) => void;
  let calls = 0;
  const read = cache.read("get_config:{}", () => {
    if (++calls === 1) return new Promise<object>((resolve) => { complete = resolve; });
    return Promise.reject(Error("offline"));
  }, readPolicy("get_config", {})!);
  const failed = assert.rejects(read, /offline/);
  await tick();
  cache.invalidate();
  complete({});
  await failed;
  assert.equal(calls, 2);
});

test("subscription projection strips tokens and nested URLs; nodes and devices keep only displayed fields", () => {
  const project = displayProjection("api", "/v1/public/user/subscribe")!;
  const value = project({ ok: true, status: 200, jwt: "secret", msg: "secret", data: { token: "secret", url: "secret", list: [
    { id: 1, status: 1, expire_time: 0, start_time: 123, token: "secret", subscribe_url: "secret", subscribe: { name: { "en-US": "Basic", "zh-CN": "基础", token: "secret" }, device_limit: 5, traffic: 100, token: "secret", url: "secret" } },
  ] } });
  assert.deepEqual(value, { ok: true, status: 200, data: { list: [
    { id: 1, status: 1, start_time: 123, expire_time: 0, subscribe: { device_limit: 5, traffic: 100, name: { "en-US": "Basic", "zh-CN": "基础" } } },
  ] } });
  assert(!JSON.stringify(value).includes("secret"));
  assert.deepEqual(displayProjection("nodes")!({ nodes: [{ name: "a", label: "JPN 01", country: "JPN", type: "anytls", password: "secret", server: "secret" }], main_node: "a", token: "secret" }), { nodes: [{ name: "a", label: "JPN 01", country: "JPN" }], main_node: "a" });
  assert.deepEqual(displayProjection("lan_clients")!({ clients: [{ mac: "aa:bb", ip: "192.0.2.1", name: "Laptop", online: true, upload: 999, token: "secret" }], direct_macs: ["aa:bb"], lan_proxy_mode: "except_listed" }), { clients: [{ mac: "aa:bb", ip: "192.0.2.1", name: "Laptop", online: true }], direct_macs: ["aa:bb"], lan_proxy_mode: "except_listed" });
});

test("restored last-good data survives offline refreshes but expires after the retention window", async () => {
  const storage = memoryStorage();
  let now = 1000;
  const cache = new AccountReadCache(storage, "router:https://router.test", () => now);
  cache.rememberStatus(status());
  const policy = readPolicy("nodes", {})!;
  const nodes = { nodes: [{ name: "a", label: "JPN 01" }], main_node: "a" };
  await cache.read("nodes:{}", async () => nodes, policy);
  now += 86400000;
  const reloaded = new AccountReadCache(storage, "router:https://router.test", () => now);
  let updates = 0;
  assert.deepEqual(await reloaded.read("nodes:{}", async () => { throw Error("offline"); }, policy, { onUpdate: () => updates++ }), nodes);
  await tick();
  assert.equal(updates, 0);
  now += DISPLAY_RETENTION;
  assert.equal(reloaded.readStatus(), null);
  await assert.rejects(reloaded.read("nodes:{}", async () => { throw Error("offline"); }, policy));
});

test("storage access failures do not break successful RPC reads", async () => {
  const denied = () => { throw Error("storage disabled"); };
  const cache = new AccountReadCache({ getItem: denied, setItem: denied, removeItem: denied }, "router:https://router.test");
  cache.rememberStatus(status());
  const result = await cache.read("nodes:{}", async () => ({ nodes: [], main_node: "" }), readPolicy("nodes", {})!);
  assert.deepEqual(result, { nodes: [], main_node: "" });
  cache.clear();
});

test("site currency persists without other site settings, restores immediately and updates after refresh", async () => {
  const storage = memoryStorage();
  const scope = "router:https://currency.test";
  const cache = new AccountReadCache(storage, scope);
  cache.rememberStatus(status());
  const policy = readPolicy("api", { method: "GET", path: "/v1/common/site/config" })!;
  assert.equal(policy.persist, true);
  await cache.read("api:site", async () => ({ ok: true, data: { currency: { currency_unit: "EUR", currency_symbol: "€", access_key: "secret-key" }, subscribe: { subscribe_domain: "secret-url" } } }), policy);
  assert(!JSON.stringify([...storage.values]).includes("secret"));
  const reloaded = new AccountReadCache(storage, scope);
  let changed = "";
  const restored = await reloaded.read("api:site", async () => ({ ok: true, data: { currency: { currency_unit: "GBP", currency_symbol: "£" } } }), policy, { onUpdate: (value) => { changed = value.data.currency.currency_unit; } });
  assert.deepEqual(restored, { ok: true, data: { currency: { currency_unit: "EUR", currency_symbol: "€" } } });
  await tick();
  assert.equal(changed, "GBP");
  const offline = new AccountReadCache(storage, scope);
  const kept = await offline.read<typeof restored>("api:site", async () => { throw Error("offline"); }, policy);
  assert.equal(kept.data.currency.currency_unit, "GBP");
});

test("restoring older user-info caches removes unused balance and unapproved fields from storage", async () => {
  const storage = memoryStorage();
  const scope = "router:https://currency.test";
  const cache = new AccountReadCache(storage, scope);
  cache.rememberStatus(status());
  const prefix = new DisplayPersistence(storage, scope).accountPrefix("alice@example.com");
  writeStored(storage, `${prefix}api:user`, { time: Date.now(), value: { ok: true, data: { id: 1, commission: 3600, balance: 9999, token: "secret" } } });
  const reloaded = new AccountReadCache(storage, scope);
  const result = await reloaded.read("api:user", async () => { throw Error("offline"); }, readPolicy("api", { method: "GET", path: "/v1/public/user/info" })!);
  assert.deepEqual(result, { ok: true, data: { id: 1, commission: 3600 } });
  const saved = JSON.stringify([...storage.values]);
  assert(!saved.includes("balance") && !saved.includes("secret"));
});
