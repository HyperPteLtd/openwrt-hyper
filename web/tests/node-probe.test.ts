import assert from "node:assert/strict";
import { test } from "node:test";
import { NodeProbe, type ProbeReply } from "../src/lib/node-probe.ts";
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r; }); return { promise, resolve }; }

test("publishes per-node results once before the whole independent job finishes", async () => {
  const replies: ProbeReply[] = [
    { result: true, id: "job", state: "running", latency: { first: 42 }, completed: 1, total: 2 },
    { result: true, id: "job", state: "done", latency: { first: 42, second: null }, completed: 2, total: 2 },
  ];
  const updates: unknown[] = [], calls: string[] = [];
  const probe = new NodeProbe(async method => { calls.push(method); return method === "latency_cancel" ? { result: true } : replies.shift()!; }, () => {}, async () => {});
  assert.deepEqual(await probe.run(["first", "second"], value => updates.push(value)), { first: 42, second: null });
  assert.deepEqual(updates, [{ first: 42 }, { second: null }]);
  assert.deepEqual(calls.slice(0, 2), ["latency", "latency_status"]);
});

test("user intent cancels pending start; the late job is cancelled without publishing or auto-picking", async () => {
  const start = deferred<ProbeReply>(); const cancelled: unknown[] = [];
  const probe = new NodeProbe(async (method, args) => {
    if (method === "latency") return start.promise;
    cancelled.push(args.id); return { result: true };
  }, () => {}, async () => {});
  const running = probe.run(["first"], () => assert.fail("stale result published"));
  probe.cancel();
  start.resolve({ result: true, id: "old", state: "done", latency: { first: 1 } });
  assert.equal(await running, undefined); assert.deepEqual(cancelled, ["old"]);
});

test("old status cannot overwrite the replacement job or cancel its progress", async () => {
  const oldStatus = deferred<ProbeReply>(); const progress: boolean[] = []; const updates: unknown[] = [];
  let starts = 0;
  const probe = new NodeProbe(async (method, args) => {
    if (method === "latency") return ++starts === 1 ? { result: true, id: "old", state: "running" } : { result: true, id: "new", state: "done", latency: { first: 30 } };
    if (method === "latency_status") return oldStatus.promise;
    return { result: true, id: args.id as string };
  }, value => progress.push(value.running), async () => {});
  const old = probe.run(["first"], value => updates.push(value));
  await Promise.resolve(); await Promise.resolve();
  const current = probe.run(["first"], value => updates.push(value));
  assert.deepEqual(await current, { first: 30 });
  oldStatus.resolve({ result: true, id: "old", state: "done", latency: { first: 99 } });
  assert.equal(await old, undefined); assert.deepEqual(updates, [{ first: 30 }]);
  assert.equal(progress.at(-1), false);
});
