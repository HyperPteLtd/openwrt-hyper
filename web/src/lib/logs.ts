export interface LogRecord {
  time: string;
  level: "info" | "warn" | "error" | "debug";
  message: string;
}

const levels: Record<string, LogRecord["level"]> = {
  INFO: "info", WARN: "warn", WARNING: "warn", ERROR: "error", FATAL: "error", DEBUG: "debug", TRACE: "debug",
};
const timestamp = /^(?:[+-]\d{4}\s+)?(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?)\s*(.*)$/;
const levelPrefix = /^(?:\[(INFO|WARN|WARNING|ERROR|FATAL|DEBUG|TRACE)\]|(INFO|WARN|WARNING|ERROR|FATAL|DEBUG|TRACE)(?:\[[\d.:]+\])?)(?:\s*:\s*|\s+|$)/i;
const categoryPrefix = /^\[[A-Z][A-Z_-]*\]\s*/;

/** Parse the backend's ordered local-time lines, retaining multiline diagnostics. */
export function parseLogs(content: string): LogRecord[] {
  const clean = content.replace(/\u001b\][^\u0007\u001b]*(?:\u0007|\u001b\\)/g, "").replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "");
  const records: LogRecord[] = [];
  for (const line of clean.split(/\r\n?|\n/)) {
    const dated = line.match(timestamp);
    const body = dated ? dated[2] : line;
    const prefix = body.match(levelPrefix);
    const category = body.match(categoryPrefix);
    if (!dated && !prefix && !category && records.length) {
      // Empty separators do not add trailing blank lines to otherwise useful records.
      if (line) records[records.length - 1].message += `\n${line}`;
      continue;
    }
    if (!body.trim() && !dated) continue;
    let message = body;
    let level: LogRecord["level"] = "info";
    while (true) {
      const severity = message.match(levelPrefix);
      if (severity) {
        level = levels[(severity[1] || severity[2]).toUpperCase()];
        message = message.slice(severity[0].length);
        continue;
      }
      const tag = message.match(categoryPrefix);
      if (tag) { message = message.slice(tag[0].length); continue; }
      break;
    }
    records.push({ time: dated?.[1] || "", level, message });
  }
  return records;
}


/** Keep one displayed line per record while making its continuation lines visible. */
export function formatLogLine(record: LogRecord, levelLabel = record.level.toUpperCase()): string {
  const message = record.message.split(/[\r\n\u2028\u2029]+/).map((line) => line.trim()).filter(Boolean).join(" ↳ ");
  return `${record.time ? `${record.time} ` : ""}[${levelLabel}]${message ? ` ${message}` : ""}`;
}
