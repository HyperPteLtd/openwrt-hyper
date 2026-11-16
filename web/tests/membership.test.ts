import assert from "node:assert/strict";
import { test } from "node:test";
import { membershipCounters } from "../src/lib/membership.ts";
import { formatBytes } from "../src/lib/format.ts";

test("membership usage uses account byte counters without needing service status", () => {
  const value = membershipCounters({ traffic: 100 * 1024 ** 3, upload: 512 * 1024 ** 2, download: 12 * 1024 ** 3, subscribe: { device_limit: 5 } });
  assert.deepEqual(value, { used: 12.5 * 1024 ** 3, total: 100 * 1024 ** 3, devices: 5 });
  assert.equal(formatBytes(value.used), "12.5 GB");
  assert.equal(formatBytes(value.total), "100 GB");
});

test("unknown and invalid counters stay unknown, distinct from explicit zero", () => {
  for (const input of [undefined, {}, { traffic: null, upload: null, download: null }, { traffic: "", upload: "", download: "" }, { traffic: -1, upload: -1, download: NaN }]) {
    assert.deepEqual(membershipCounters(input), { used: null, total: null, devices: null });
  }
  assert.equal(membershipCounters({ upload: 0 }).used, null, "a missing download count is not zero");
  assert.equal(membershipCounters({ download: 1024 }).used, null, "a partial total is not presented as complete");
  assert.deepEqual(membershipCounters({ traffic: 0, upload: 0, download: 0, subscribe: { device_limit: 0 } }), { used: 0, total: 0, devices: 0 });
  assert.equal(formatBytes(membershipCounters({}).used), "--");
  assert.equal(formatBytes(membershipCounters({ upload: 0, download: 0 }).used), "0 B");
  assert.deepEqual(membershipCounters({ traffic: "1024", upload: "256", download: "768" }), { used: 1024, total: 1024, devices: null });
  assert.equal(membershipCounters({ upload: Number.MAX_VALUE, download: Number.MAX_VALUE }).used, null);
});
