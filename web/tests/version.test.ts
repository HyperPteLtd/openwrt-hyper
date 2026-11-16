import assert from "node:assert/strict";
import { test } from "node:test";
import { formatVersion } from "../src/lib/format.ts";

test("display versions hide package revisions in current and persisted status", () => {
  for (const [input, expected] of [
    ["0.4.0-r1", "0.4.0"],
    [" v0.4.0-r12 ", "0.4.0"],
    ["0.4.0", "0.4.0"],
    ["0.4.0-beta.1-r2", "0.4.0-beta.1"],
    ["0.4.0-beta.1", "0.4.0-beta.1"],
    ["1.0.0-1", "1.0.0-1"],
    [null, "--"],
    [undefined, "--"],
    ["", "--"],
  ]) assert.equal(formatVersion(input), expected);
});
