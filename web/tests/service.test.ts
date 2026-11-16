import assert from "node:assert/strict";
import { test } from "node:test";
import { liveUptime, operationPending } from "../src/lib/service.ts";
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
