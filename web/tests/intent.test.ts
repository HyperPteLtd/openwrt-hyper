import assert from "node:assert/strict";
import { test } from "node:test";
import { LatestAction } from "../src/lib/intent.ts";
import { invalidateAccountWork } from "../src/lib/account-epoch.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}
const turn = () => new Promise<void>(resolve => setTimeout(resolve, 0));

test("start/stop keeps only the latest waiting intent and ignores stale success", async () => {
  const requests: { value: boolean; work: ReturnType<typeof deferred<number>> }[] = [];
  const accepted: boolean[] = [];
  const queue = new LatestAction<boolean, number>(value => {
    const work = deferred<number>(); requests.push({ value, work }); return work.promise;
  }, value => accepted.push(value), error => assert.fail(error.message));
  queue.enqueue(true);
  queue.enqueue(false);
  queue.enqueue(true);
  queue.enqueue(false);
  assert.equal(requests.length, 1);
  requests[0].work.resolve(1);
  await turn();
  assert.deepEqual(requests.map(request => request.value), [true, false]);
  assert.deepEqual(accepted, []);
  requests[1].work.resolve(2);
  await turn();
  assert.deepEqual(accepted, [false]);
});

test("an old failure never rolls back a newer mode and does not block its write", async () => {
  const work = deferred<void>();
  const applied: string[] = [], errors: string[] = [], writes: string[] = [];
  const queue = new LatestAction<string, void>(value => {
    writes.push(value); return writes.length === 1 ? work.promise : Promise.resolve();
  }, value => applied.push(value), error => errors.push(error.message));
  queue.enqueue("global"); queue.enqueue("dns");
  work.reject(Error("old request failed"));
  await turn();
  assert.deepEqual(writes, ["global", "dns"]);
  assert.deepEqual(applied, ["dns"]);
  assert.deepEqual(errors, []);
});

test("only the latest failure is reported, and another input retries immediately", async () => {
  const errors: string[] = [], applied: string[] = [];
  const queue = new LatestAction<string, void>(value => value === "bad" ? Promise.reject(Error("latest failed")) : Promise.resolve(), value => applied.push(value), error => errors.push(error.message));
  queue.enqueue("bad"); await turn();
  assert.deepEqual(errors, ["latest failed"]);
  queue.enqueue("good"); await turn();
  assert.deepEqual(applied, ["good"]);
});

test("logout drops a queued start and ignores the in-flight account's acknowledgment", async () => {
  const old = deferred<void>();
  const writes: string[] = [], accepted: string[] = [], errors: string[] = [];
  const queue = new LatestAction<string, void>(value => {
    writes.push(value);
    return value === "old-stop" ? old.promise : Promise.resolve();
  }, value => accepted.push(value), error => errors.push(error.message));
  queue.enqueue("old-stop"); queue.enqueue("old-start");
  invalidateAccountWork();
  old.resolve(); await turn();
  assert.deepEqual(writes, ["old-stop"]);
  assert.deepEqual(accepted, []); assert.deepEqual(errors, []);
  queue.enqueue("new-account-start"); await turn();
  assert.deepEqual(accepted, ["new-account-start"]);
});

test("an old selection failure cannot erase a new account's pending selection", async () => {
  const old = deferred<void>();
  const writes: string[] = [], accepted: string[] = [], errors: string[] = [];
  const queue = new LatestAction<string, void>(value => {
    writes.push(value); return value === "old-node" ? old.promise : Promise.resolve();
  }, value => accepted.push(value), error => errors.push(error.message));
  queue.enqueue("old-node"); queue.enqueue("discarded-node");
  invalidateAccountWork();
  queue.enqueue("new-node");
  old.reject(Error("old failure")); await turn();
  assert.deepEqual(writes, ["old-node", "new-node"]);
  assert.deepEqual(accepted, ["new-node"]); assert.deepEqual(errors, []);
});
