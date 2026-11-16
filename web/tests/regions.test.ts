import assert from "node:assert/strict";
import { test } from "node:test";
import { nodeLabel, regionCode, regionFlag, regionName, nodeRegion } from "../src/lib/regions.ts";

test("ISO codes render as regions and flags without changing node identifiers", () => {
  assert.equal(nodeLabel("THA 01"), "泰国 01");
  assert.equal(nodeLabel("DEU 01"), "德国 01");
  assert.equal(nodeLabel("USA 01"), "美国 01");
  assert.equal(nodeLabel("JPN 01"), "日本 01");
  assert.match(nodeLabel("HKG 01"), /香港.*01/);
  assert.equal(nodeLabel("SGP 01"), "新加坡 01");
  assert.equal(regionCode("tha"), "TH");
  assert.equal(regionCode("US"), "US");
  assert.equal(regionFlag("THA"), "🇹🇭");
  assert.equal(regionName("not-a-region"), "");
  assert.equal(regionFlag("ZZ"), "");
  assert.equal(nodeLabel("东京专线 01"), "东京专线 01");
  assert.equal(nodeRegion({ label: "THA 01" }), "TH");
});
