export interface TrafficSample {
  /** Unix time in milliseconds, matching Date.now(). Rates are bytes per second. */
  time: number;
  up: number;
  down: number;
}

export const TRAFFIC_WINDOW_MS = 60_000;
export const TRAFFIC_CHART_HEIGHT = 112;

function rateCeiling(peak: number): number {
  if (peak <= 2) return 2;
  const unit = 1024 ** Math.min(4, Math.max(0, Math.floor(Math.log(peak) / Math.log(1024))));
  const scaled = peak / unit;
  const power = 10 ** Math.floor(Math.log10(scaled));
  const step = [1, 2, 2.5, 5, 10].find((value) => value * power >= scaled) ?? 10;
  return step * power * unit;
}

/** Monotone cubic interpolation passes through samples without inventing larger peaks. */
function smoothPath(points: { x: number; upY: number; downY: number }[], direction: "upY" | "downY"): string {
  if (!points.length) return "";
  const format = (value: number) => value.toFixed(2);
  let path = `M${format(points[0].x)},${format(points[0][direction])}`;
  if (points.length === 1) return path;
  const slopes = points.slice(1).map((point, index) => (point[direction] - points[index][direction]) / (point.x - points[index].x));
  const tangents = points.map((_, index) => index === 0 ? slopes[0] : index === points.length - 1 ? slopes.at(-1)! : slopes[index - 1] * slopes[index] <= 0 ? 0 : (slopes[index - 1] + slopes[index]) / 2);
  for (let index = 0; index < slopes.length; index++) {
    const slope = slopes[index];
    if (slope === 0) { tangents[index] = 0; tangents[index + 1] = 0; continue; }
    const alpha = tangents[index] / slope;
    const beta = tangents[index + 1] / slope;
    const magnitude = Math.hypot(alpha, beta);
    if (magnitude > 3) {
      const scale = 3 / magnitude;
      tangents[index] = scale * alpha * slope;
      tangents[index + 1] = scale * beta * slope;
    }
  }
  for (let index = 0; index < points.length - 1; index++) {
    const from = points[index];
    const to = points[index + 1];
    const third = (to.x - from.x) / 3;
    path += ` C${format(from.x + third)},${format(from[direction] + tangents[index] * third)} ${format(to.x - third)},${format(to[direction] - tangents[index + 1] * third)} ${format(to.x)},${format(to[direction])}`;
  }
  return path;
}

/** Real samples only: positions reflect elapsed time, never sample count or padded zeros. */
export function buildTrafficChart(samples: readonly TrafficSample[], now: number, width = 600) {
  const right = Math.max(160, width) - 8;
  const plot = { left: 62, right, top: 8, bottom: 86 };
  const start = now - TRAFFIC_WINDOW_MS;
  const byTime = new Map<number, TrafficSample>();
  for (const sample of samples) {
    if (!Number.isFinite(sample.time) || !Number.isFinite(sample.up) || !Number.isFinite(sample.down)) continue;
    if (sample.time < start || sample.time > now || sample.up < 0 || sample.down < 0) continue;
    byTime.set(sample.time, sample);
  }
  const visible = [...byTime.values()].sort((a, b) => a.time - b.time);
  let peak = 0;
  for (const sample of visible) peak = Math.max(peak, sample.up, sample.down);
  const ceiling = rateCeiling(peak);
  const x = (time: number) => plot.left + ((time - start) / TRAFFIC_WINDOW_MS) * (plot.right - plot.left);
  const y = (value: number) => plot.bottom - (value / ceiling) * (plot.bottom - plot.top);
  const points = visible.map((sample) => ({ ...sample, x: x(sample.time), upY: y(sample.up), downY: y(sample.down) }));
  const path = (direction: "upY" | "downY") => smoothPath(points, direction);
  return {
    samples: visible, points, plot, ceiling,
    uploadPath: path("upY"), downloadPath: path("downY"),
    rateTicks: [ceiling, ceiling / 2, 0].map((value) => ({ value, y: y(value) })),
    timeTicks: [60, 30, 0].map((secondsAgo) => ({ secondsAgo, time: now - secondsAgo * 1000, x: x(now - secondsAgo * 1000) })),
  };
}
