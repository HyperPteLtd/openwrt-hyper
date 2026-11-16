import assert from "node:assert/strict";
import { test } from "node:test";
import { controlOutcome, liveUptime, operationPending } from "../src/lib/service.ts";
import { formatUptime } from "../src/lib/format.ts";

test("uptime advances each second, resyncs after restart, and caps stale samples", () => {
  assert.equal(liveUptime(0, 1000, 1999), 0);
  assert.equal(liveUptime(0, 1000, 2000), 1);
  assert.equal(liveUptime(35, 1000, 4000), 38);
  assert.equal(liveUptime(0, 4000, 4000), 0);
  assert.equal(liveUptime(35, 1000, 61000), 45);
  assert.equal(liveUptime(null, 1000, 5000), null);
  assert.equal(liveUptime(NaN, 1000, 5000), null);
  assert.equal(formatUptime(0), "00:00:00");
  assert.equal(formatUptime(1), "00:00:01");
  assert.equal(formatUptime(3661), "01:01:01");
  assert.equal(formatUptime(90061), "1 天 01:01:01");
  assert.equal(formatUptime(null), "--");
});

test("queued control is pending until the worker reports completion or failure", () => {
  const op = { id: 1, action: "start" as const, time: 1 };
  assert.equal(operationPending({ ...op, state: "queued" }), true);
  assert.equal(operationPending({ ...op, state: "running" }), true);
  assert.equal(operationPending({ ...op, state: "done" }), false);
  assert.equal(operationPending({ ...op, state: "failed" }), false);
  assert.equal(operationPending(null), false);
});

test("controls wait for acknowledgment and actual completion, including a superseding restart", () => {
  const op = { id: 2, action: "start" as const, time: 1 };
  assert.equal(controlOutcome({ running: true }, true, undefined), "pending");
  assert.equal(controlOutcome({ running: true, operation: { ...op, state: "running" } }, true, 2), "pending");
  assert.equal(controlOutcome({ running: false, operation: { ...op, id: 1, state: "failed" } }, true, 2), "pending");
  assert.equal(controlOutcome({ running: true, operation: { ...op, state: "done" } }, true, 2), "done");
  assert.equal(controlOutcome({ running: false, operation: { ...op, state: "done" } }, true, 2), "failed");
  assert.equal(controlOutcome({ running: false, operation: { ...op, id: 3, action: "restart", state: "failed" } }, true, 2), "failed");
  assert.equal(controlOutcome({ running: false, operation: { ...op, action: "stop", state: "done" } }, false, 2), "done");
});
