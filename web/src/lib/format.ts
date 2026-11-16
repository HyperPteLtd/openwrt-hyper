import { t as tr } from "./i18n.ts";
export function formatBytes(n: number | null | undefined): string {
  if (n == null || !isFinite(n)) return "--";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[i]}`;
}

export function formatRate(bytesPerSecond: number | null | undefined): string {
  return bytesPerSecond == null ? "--" : `${formatBytes(bytesPerSecond)}/s`;
}

export function formatUptime(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return "--";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  const hm = `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return d > 0 ? tr("{0} 天 {1}", [d, hm]) : hm;
}

export function formatAgo(epoch: number | null | undefined): string {
  if (!epoch) return tr("从未");
  const s = Math.max(0, Math.floor(Date.now() / 1000) - epoch);
  if (s < 60) return tr("刚刚");
  if (s < 3600) return tr("{0} 分钟前", [Math.floor(s / 60)]);
  if (s < 86400) return tr("{0} 小时前", [Math.floor(s / 3600)]);
  return tr("{0} 天前", [Math.floor(s / 86400)]);
}

/** Latency colour classes from the design: <=100 ms good, slower warn, none = timeout */
export function latencyTone(ms: number | null | undefined): "good" | "warn" | "none" {
  if (ms == null || ms <= 0) return "none";
  return ms <= 100 ? "good" : "warn";
}
