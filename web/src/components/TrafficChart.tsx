import { useSignal } from "@preact/signals";
import { useId, useLayoutEffect, useRef } from "preact/hooks";
import { t as tr } from "../lib/i18n";
import { formatRate } from "../lib/format";
import { buildTrafficChart, TRAFFIC_CHART_HEIGHT, type TrafficSample } from "../lib/traffic-chart";
import "./TrafficChart.css";

export type { TrafficSample } from "../lib/traffic-chart";

/** A 60-second rate chart. Callers supply actual timestamped samples in B/s. */
export function TrafficChart({ samples }: { samples: readonly TrafficSample[] }) {
  const plot = useRef<HTMLDivElement>(null);
  const width = useSignal(320);
  const titleId = useId();
  const descId = useId();
  useLayoutEffect(() => {
    const node = plot.current;
    if (!node) return;
    const measure = () => {
      const next = Math.max(160, Math.round(node.getBoundingClientRect().width));
      if (next !== width.value) width.value = next;
    };
    measure();
    const resize = new ResizeObserver(measure);
    resize.observe(node);
    return () => resize.disconnect();
  }, []);
  const chart = buildTrafficChart(samples, Date.now(), width.value);
  const latest = chart.points.at(-1);
  const summary = latest ? tr("{0} 个采样点，下载 {1}，上传 {2}", [chart.points.length, formatRate(latest.down), formatRate(latest.up)]) : tr("暂无流量数据");

  return (
    <figure class="traffic-chart">
      <figcaption class="traffic-chart-legend">
        <span><i class="traffic-chart-key" aria-hidden="true" />{tr("下载")} <span class="traffic-chart-live">{formatRate(latest?.down)}</span></span>
        <span><i class="traffic-chart-key upload" aria-hidden="true" />{tr("上传")} <span class="traffic-chart-live">{formatRate(latest?.up)}</span></span>
      </figcaption>
      <div class="traffic-chart-plot" ref={plot}>
        <svg class="traffic-chart-svg" viewBox={`0 0 ${width.value} ${TRAFFIC_CHART_HEIGHT}`} preserveAspectRatio="none" role="img" aria-labelledby={`${titleId} ${descId}`}>
          <title id={titleId}>{tr("最近 60 秒的实时流量")}</title>
          <desc id={descId}>{summary}</desc>
          <g aria-hidden="true">
            {chart.rateTicks.map((tick) => <g key={tick.value}>
              <line class="traffic-chart-grid" x1={chart.plot.left} x2={chart.plot.right} y1={tick.y} y2={tick.y} />
              <text class="traffic-chart-axis" x={chart.plot.left - 8} y={tick.y + 3} text-anchor="end">{formatRate(tick.value)}</text>
            </g>)}
            {chart.timeTicks.map((tick) => <g key={tick.secondsAgo}>
              <line class="traffic-chart-grid vertical" x1={tick.x} x2={tick.x} y1={chart.plot.top} y2={chart.plot.bottom} />
              <text class="traffic-chart-axis" x={tick.x} y={TRAFFIC_CHART_HEIGHT - 5} text-anchor={tick.secondsAgo === 60 ? "start" : tick.secondsAgo === 0 ? "end" : "middle"}>{tick.secondsAgo ? tr("{0} 秒前", [tick.secondsAgo]) : tr("现在")}</text>
            </g>)}
            {latest && <>
              <path class="traffic-chart-line download" d={chart.downloadPath} />
              <path class="traffic-chart-line upload" d={chart.uploadPath} />
              <circle class="traffic-chart-point download" cx={latest.x} cy={latest.downY} r="3.5" />
              <circle class="traffic-chart-point upload" cx={latest.x} cy={latest.upY} r="2.5" />
            </>}
          </g>
        </svg>
        {!latest && <div class="traffic-chart-empty" aria-hidden="true"><span>{tr("暂无流量数据")}</span></div>}
      </div>
    </figure>
  );
}
