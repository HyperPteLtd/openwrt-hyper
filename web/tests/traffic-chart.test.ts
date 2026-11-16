import assert from "node:assert/strict";
import { test } from "node:test";
import { buildTrafficChart } from "../src/lib/traffic-chart.ts";

const now = 120_000;

test("no samples produces empty paths, never artificial zero traffic", () => {
  const chart = buildTrafficChart([], now);
  assert.equal(chart.points.length, 0);
  assert.equal(chart.downloadPath, "");
  assert.equal(chart.uploadPath, "");
  assert(chart.rateTicks.every((tick) => Number.isFinite(tick.y)));
});

test("a single sample keeps its true position without extending a line", () => {
  const chart = buildTrafficChart([{ time: now - 15_000, up: 128, down: 256 }], now);
  assert.equal(chart.points.length, 1);
  assert.equal(chart.points[0].x, chart.plot.left + .75 * (chart.plot.right - chart.plot.left));
  assert.match(chart.downloadPath, /^M[\d.]+,[\d.]+$/);
  assert(!chart.downloadPath.includes("L"));
});

test("all-zero samples remain valid baseline data with a finite scale", () => {
  const chart = buildTrafficChart([{ time: now - 3000, up: 0, down: 0 }, { time: now, up: 0, down: 0 }], now);
  assert.equal(chart.points.length, 2);
  assert(chart.ceiling > 0);
  assert(chart.points.every((point) => point.upY === chart.plot.bottom && point.downY === chart.plot.bottom));
  assert(!/NaN|Infinity/.test(chart.downloadPath + chart.uploadPath));
});

test("both directions determine the scale and a peak stays inside the plot", () => {
  const peak = 8 * 1024 ** 2;
  const chart = buildTrafficChart([{ time: now - 3000, up: peak, down: 100 }, { time: now, up: 20, down: 1024 }], now, 390);
  assert(chart.ceiling >= peak);
  assert(chart.points.every((point) => point.upY >= chart.plot.top && point.downY >= chart.plot.top));
  assert.equal(chart.points.at(-1)?.x, chart.plot.right);
  assert.deepEqual(chart.timeTicks.map((tick) => tick.secondsAgo), [60, 30, 0]);
});

test("keeps only real valid samples inside the last 60 seconds without mutating input", () => {
  const samples = [
    { time: now, up: 1, down: 1 }, { time: now - 60_000, up: 2, down: 2 },
    { time: now - 60_001, up: 3, down: 3 }, { time: now + 1, up: 4, down: 4 },
    { time: now - 30_000, up: NaN, down: 1 }, { time: now - 15_000, up: -1, down: 1 },
    { time: now, up: 5, down: 6 },
  ];
  const original = structuredClone(samples);
  const chart = buildTrafficChart(samples, now);
  assert.deepEqual(chart.samples, [samples[1], samples[6]]);
  assert.deepEqual(samples, original);
  assert.equal(chart.points[0].x, chart.plot.left);
});


test("smooth curves pass through samples without overshooting peaks or flat segments", () => {
  const chart = buildTrafficChart([
    { time: now - 50_000, up: 0, down: 1000 },
    { time: now - 40_000, up: 10, down: 0 },
    { time: now - 39_000, up: 900, down: 2000 },
    { time: now - 20_000, up: 900, down: 2000 },
    { time: now, up: 0, down: 100 },
  ], now);
  for (const [path, direction] of [[chart.uploadPath, "upY"], [chart.downloadPath, "downY"]] as const) {
    const segments = path.split(" C").slice(1);
    assert.equal(segments.length, chart.points.length - 1);
    segments.forEach((segment, index) => {
      const [x1, y1, x2, y2, x, y] = segment.split(/[ ,]/).map(Number);
      const from = chart.points[index];
      const to = chart.points[index + 1];
      const low = Math.min(from[direction], to[direction]) - .01;
      const high = Math.max(from[direction], to[direction]) + .01;
      assert(y1 >= low && y1 <= high && y2 >= low && y2 <= high);
      assert(x1 >= from.x && x2 <= to.x);
      assert.equal(x, +to.x.toFixed(2));
      assert.equal(y, +to[direction].toFixed(2));
    });
  }
});
