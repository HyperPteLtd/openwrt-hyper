/* Check-in calendar maths. Run: npm test (Node >= 23.6 runs .ts directly). */
import assert from "node:assert/strict";
import { test } from "node:test";
import { dateKey, monthRange, monthView, previousDay, streak } from "../src/lib/calendar.ts";

test("dateKey is the local date", () => {
  assert.equal(dateKey(new Date(2026, 10, 9, 0, 0, 1)), "2026-11-09");
  assert.equal(dateKey(new Date(2026, 10, 9, 23, 59, 59)), "2026-11-09");
  assert.equal(dateKey(new Date(2026, 0, 5)), "2026-01-05");
});

test("previousDay crosses months, years, leap days and DST changes", () => {
  assert.equal(previousDay("2026-11-09"), "2026-11-08");
  assert.equal(previousDay("2026-03-01"), "2026-02-28");
  assert.equal(previousDay("2024-03-01"), "2024-02-29");
  assert.equal(previousDay("2026-01-01"), "2025-12-31");
  /* US DST starts 2026-03-08 and ends 2026-11-01 (tests run with TZ=America/New_York) */
  assert.equal(previousDay("2026-03-09"), "2026-03-08");
  assert.equal(previousDay("2026-03-08"), "2026-03-07");
  assert.equal(previousDay("2026-11-02"), "2026-11-01");
  assert.equal(previousDay("2026-11-01"), "2026-10-31");
});

test("monthView lays out a Monday-first month", () => {
  const checked = new Set(["2026-11-01", "2026-11-07", "2026-11-09", "2026-10-31"]);
  const v = monthView(2026, 10, checked, "2026-11-09");
  assert.equal(v.offset, 6, "2026-11-01 is a Sunday");
  assert.equal(v.days.length, 30);
  assert.equal(v.count, 3, "October's check-in is not counted");
  const state = (d: number) => v.days[d - 1].state;
  assert.equal(state(1), "checked");
  assert.equal(state(2), "missed");
  assert.equal(state(7), "checked");
  assert.equal(state(9), "today-checked");
  assert.equal(state(10), "future");
  assert.equal(v.days[29].key, "2026-11-30");

  const open = monthView(2026, 10, new Set(), "2026-11-09");
  assert.equal(open.days[8].state, "today");
  assert.equal(open.count, 0);

  assert.equal(monthView(2026, 5, checked, "2026-11-09").offset, 0, "2026-06-01 is a Monday");
  assert.equal(monthView(2026, 1, checked, "2026-11-09").days.length, 28);
  assert.equal(monthView(2024, 1, checked, "2026-11-09").days.length, 29);
  assert.equal(monthView(2024, 1, checked, "2026-11-09").offset, 3, "2024-02-01 is a Thursday");
  assert.ok(monthView(2026, 9, checked, "2026-11-09").days.every((d) => d.state === "missed" || d.state === "checked"));
  assert.ok(monthView(2026, 11, checked, "2026-11-09").days.every((d) => d.state === "future"));
});

test("streak counts back from today, or from yesterday while today is open", () => {
  const today = "2026-11-09";
  assert.equal(streak(new Set(), today), 0);
  assert.equal(streak(new Set(["2026-11-07", "2026-11-08"]), today), 2);
  assert.equal(streak(new Set(["2026-11-07", "2026-11-08", today]), today), 3);
  assert.equal(streak(new Set(["2026-11-06", "2026-11-08"]), today), 1);
  assert.equal(streak(new Set(["2026-11-07"]), today), 0, "a gap yesterday breaks it");
  assert.equal(streak(new Set(["2026-10-30", "2026-10-31", "2026-11-01"]), "2026-11-01"), 3);
  assert.equal(streak(new Set(["2026-11-08", "2026-11-10"]), today), 1, "a future date does not count");
});

test("monthRange runs from the first check-in to this month", () => {
  assert.deepEqual(monthRange([], "2026-11-09"), { first: [2026, 10], last: [2026, 10] });
  assert.deepEqual(monthRange(["2026-11-01", "2025-12-30", "2026-03-04"], "2026-11-09"), { first: [2025, 11], last: [2026, 10] });
  /* a date ahead of this browser's clock never moves the range forward */
  assert.deepEqual(monthRange(["2026-12-01"], "2026-11-30"), { first: [2026, 10], last: [2026, 10] });
});
