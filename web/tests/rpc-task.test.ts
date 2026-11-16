import assert from "node:assert/strict";
import { test } from "node:test";
import { waitForRpcTask } from "../src/lib/rpc-task.ts";

function clock(step = 250) {
  let time = 0;
  return { now: () => time, sleep: async () => { time += step; } };
}

test("deferred results return only after completion without replaying the action", async () => {
  let polls = 0;
  const result = await waitForRpcTask(180, async () => ++polls === 3
    ? { state: "done", data: { nodes: 6 } } : { state: "running" }, () => {}, clock());
  assert.deepEqual(result, { nodes: 6 });
  assert.equal(polls, 3);
});

test("installation downloads can complete after the ordinary task deadline", async () => {
  let polls = 0;
  const result = await waitForRpcTask(480, async () => ++polls === 3
    ? { state: "done", data: { started: true } } : { state: "running" }, () => {}, clock(70000));
  assert.deepEqual(result, { started: true });
  await assert.rejects(waitForRpcTask(180, async () => ({ state: "running" }), () => {}, clock(70000)), /timeout/);
});

test("only the supported server deadlines are accepted and backend failures terminate polling", async () => {
  const timer = clock(100000);
  await assert.rejects(waitForRpcTask(Infinity, async () => ({ state: "running" }), () => {}, timer), /timeout/);
  assert.equal(timer.now(), 200000);
  let polls = 0;
  await assert.rejects(waitForRpcTask(180, async () => { polls++; return { state: "failed", error: "session_changed" }; }, () => {}, clock()), /session_changed/);
  assert.equal(polls, 1);
});

test("identity changes during the polling delay prevent the next status request", async () => {
  let current = true, polls = 0;
  const guard = () => { if (!current) throw new Error("obsolete"); };
  await assert.rejects(waitForRpcTask(180, async () => { polls++; return { state: "done", data: "old" }; },
    guard, { now: () => 0, sleep: async () => { current = false; } }), /obsolete/);
  assert.equal(polls, 0);
});

test("identity changes while awaiting status cannot return old-account results", async () => {
  let current = true;
  await assert.rejects(waitForRpcTask(180, async () => { current = false; return { state: "done", data: "old" }; },
    () => { if (!current) throw new Error("obsolete"); }, clock()), /obsolete/);
});
