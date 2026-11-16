import assert from "node:assert/strict";
import { test } from "node:test";
import { formatLogLine, parseLogs } from "../src/lib/logs.ts";

test("parses local timestamps with an optional numeric timezone without reordering", () => {
  assert.deepEqual(parseLogs("2026-11-16 10:00:01 [DAEMON] Ready\n+0800 2026-11-16 10:00:02 WARN Slow"), [
    { time: "2026-11-16 10:00:01", level: "info", message: "Ready" },
    { time: "2026-11-16 10:00:02", level: "warn", message: "Slow" },
  ]);
});

test("keeps stack traces and other continuation lines in their original record", () => {
  assert.deepEqual(parseLogs("2026-11-16 10:00:01 ERROR Failed\n  at /script:42\ncaused by a timeout\n2026-11-16 10:00:02 [SYNC] Ready\n"), [
    { time: "2026-11-16 10:00:01", level: "error", message: "Failed\n  at /script:42\ncaused by a timeout" },
    { time: "2026-11-16 10:00:02", level: "info", message: "Ready" },
  ]);
});

test("normalizes severities including core prefixes and removes technical category tags", () => {
  const input = ["[DAEMON] INFO Ready", "WARN Slow", "WARNING: Slow", "ERROR[0000] Failed", "FATAL[0000] Exit", "DEBUG Detail", "TRACE Detail", "[SYNC] Finished"];
  assert.deepEqual(parseLogs(input.join("\n")).map(({ level, message }) => ({ level, message })), [
    { level: "info", message: "Ready" }, { level: "warn", message: "Slow" }, { level: "warn", message: "Slow" },
    { level: "error", message: "Failed" }, { level: "error", message: "Exit" },
    { level: "debug", message: "Detail" }, { level: "debug", message: "Detail" }, { level: "info", message: "Finished" },
  ]);
});

test("strips ANSI colors and OSC terminal controls while preserving readable text", () => {
  assert.deepEqual(parseLogs("\u001b[31m2026-11-16 10:00:01 ERROR\u001b[0m Failed\r\n\u001b]8;;https://example.com\u0007  detail\u001b]8;;\u0007"), [
    { time: "2026-11-16 10:00:01", level: "error", message: "Failed\n  detail" },
  ]);
});

test("unknown and malformed timestamps remain readable without invented dates", () => {
  assert.deepEqual(parseLogs("legacy log without date\n2026/11/16 at 10:00:01: still useful\nINFO Ready"), [
    { time: "", level: "info", message: "legacy log without date\n2026/11/16 at 10:00:01: still useful" },
    { time: "", level: "info", message: "Ready" },
  ]);
  assert.deepEqual(parseLogs("\n\r\n"), []);
});


test("formats every record on one line with visible continuation separators", () => {
  const records = parseLogs("2026-11-16 10:00:01 ERROR Failed\n  at /script:42\ncaused by a timeout\n2026-11-16 10:00:02 INFO Ready");
  const text = records.map((record) => formatLogLine(record)).join("\n");
  assert.equal(text, "2026-11-16 10:00:01 [ERROR] Failed ↳ at /script:42 ↳ caused by a timeout\n2026-11-16 10:00:02 [INFO] Ready");
  assert.equal(text.split("\n").length, records.length);
  assert.equal(records[0].message, "Failed\n  at /script:42\ncaused by a timeout");
});

test("single-line formatting accepts translated levels without inventing a timestamp", () => {
  assert.equal(formatLogLine({ time: "", level: "warn", message: "First\r\n Second\u2028Third\u2029 Fourth" }, "警告"), "[警告] First ↳ Second ↳ Third ↳ Fourth");
  assert.equal(formatLogLine({ time: "", level: "info", message: "" }), "[INFO]");
});
