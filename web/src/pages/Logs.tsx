import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { CardHeader, Notice } from "../components/ui";
import { call } from "../lib/rpc";
import { serviceText } from "../lib/display";
import { formatLogLine, parseLogs, type LogRecord } from "../lib/logs";
import "./Logs.css";
import { errorText } from "../lib/errors";

interface Health {
  service?: { ok: boolean };
  node?: { delay?: number; message?: string } | null;
  dns?: { ok: boolean };
  // Probed through the core while it runs, so the rules pick each route; straight out otherwise.
  via_core?: boolean;
  sites?: Record<string, { ok: boolean; ms?: number | null }>;
}

// Sites on both sides of the rules, mainland first (hyperd healthSites).
const SITES: { id: string; label: string; host: string }[] = [
  { id: "baidu", label: tr("百度"), host: "www.baidu.com" },
  { id: "netease", label: tr("网易云音乐"), host: "music.163.com" },
  { id: "google", label: "Google", host: "www.google.com" },
  { id: "youtube", label: "YouTube", host: "www.youtube.com" },
  { id: "github", label: "GitHub", host: "github.com" },
];

type LogLevel = "all" | LogRecord["level"];
const LEVELS: { id: LogLevel; label: string }[] = [
  { id: "all", label: tr("全部") }, { id: "error", label: tr("错误") },
  { id: "warn", label: tr("警告") }, { id: "info", label: tr("信息") }, { id: "debug", label: tr("调试") },
];
const DIAG_TYPES = [
  { id: "ping", label: "Ping" }, { id: "traceroute", label: tr("路由追踪") },
  { id: "nslookup", label: tr("DNS 查询") }, { id: "http", label: tr("HTTP 测试") },
];

export function Logs() {
  const msg = useSignal("");
  const health = useSignal<Health | null>(null);
  const checking = useSignal(false);
  const kind = useSignal("ping");
  const target = useSignal("www.google.com");
  const via = useSignal("proxy");
  const output = useSignal("");
  const running = useSignal(false);
  const level = useSignal<LogLevel>("all");
  const content = useSignal("");
  const loadingLogs = useSignal(true);
  const clearing = useSignal(false);
  const active = useRef(true);
  const logRequest = useRef(0);
  const healthRequest = useRef(0);
  const diagRequest = useRef(0);

  const loadLog = async () => {
    const request = ++logRequest.current;
    loadingLogs.value = true;
    try {
      const r = await call<{ content?: string; error?: string }>("logs", { type: "combined", lines: 300 });
      if (!active.current || request !== logRequest.current) return;
      if (r.error) throw new Error(errorText(r.error));
      content.value = r.content || "";
    } catch (e) {
      if (active.current && request === logRequest.current) msg.value = (e as Error).message;
    } finally {
      if (active.current && request === logRequest.current) loadingLogs.value = false;
    }
  };

  useEffect(() => {
    active.current = true;
    void loadLog();
    return () => { active.current = false; logRequest.current++; healthRequest.current++; diagRequest.current++; };
  }, []);

  const runHealth = async () => {
    const request = ++healthRequest.current;
    checking.value = true;
    msg.value = "";
    try {
      const result = await call<Health>("health_check");
      if (active.current && request === healthRequest.current) health.value = result;
    } catch (e) {
      if (active.current && request === healthRequest.current) msg.value = (e as Error).message;
    } finally {
      if (active.current && request === healthRequest.current) { checking.value = false; void loadLog(); }
    }
  };

  const runDiag = async () => {
    if (!target.value.trim()) return;
    const request = ++diagRequest.current;
    running.value = true;
    output.value = "";
    try {
      const r = await call<{ result: boolean; output?: string; error?: string }>("diag", { type: kind.value, target: target.value.trim(), via: via.value });
      if (active.current && request === diagRequest.current) output.value = r.output || errorText(r.error);
    } catch (e) {
      if (active.current && request === diagRequest.current) output.value = (e as Error).message;
    } finally {
      if (active.current && request === diagRequest.current) { running.value = false; void loadLog(); }
    }
  };

  const clean = async () => {
    if (clearing.value) return;
    logRequest.current++;
    clearing.value = true;
    msg.value = "";
    try {
      const r = await call<{ result?: boolean; error?: string }>("log_clean", { type: "combined" });
      if (r.error || r.result === false) throw new Error(errorText(r.error, tr("操作没有成功，请查看日志")));
      if (active.current) await loadLog();
    } catch (e) {
      if (active.current) msg.value = (e as Error).message;
    } finally {
      if (active.current) clearing.value = false;
    }
  };

  const h = health.value;
  const checks = [
    { label: tr("服务进程"), ok: h?.service?.ok, detail: h ? (h.service?.ok ? tr("代理服务运行中") : tr("代理服务未运行")) : "" },
    { label: tr("节点连通"), ok: !!h?.node?.delay, detail: h ? (h.node?.delay ? tr("当前节点 {0} ms", [h.node.delay]) : h.node?.message || tr("当前节点无法连通")) : "" },
    { label: tr("DNS 解析"), ok: h?.dns?.ok, detail: h ? (h.dns?.ok ? tr("解析正常") : tr("解析失败")) : "" },
    ...SITES.map((site) => {
      const r = h?.sites?.[site.id];
      const route = h?.via_core ? tr("按规则分流") : tr("直连");
      return { label: site.label, ok: r?.ok, detail: h ? (r?.ok ? `${site.host} · ${route} · ${r.ms} ms` : `${site.host} · ${tr("无法访问")}`) : "" };
    }),
  ];
  const records = parseLogs(serviceText(content.value)).filter((record) => level.value === "all" || record.level === level.value);
  const editDiagnostic = () => { diagRequest.current++; running.value = false; output.value = ""; };

  return (
    <main class="page diagnostics-page">
      <Notice text={msg.value} />
      <div class="diagnostics-layout">
        <div class="diagnostics-tools">
          <section class="card">
            <CardHeader title={tr("一键体检")}>
              <button type="button" class="btn primary" aria-busy={checking.value} onClick={() => runHealth()}>{checking.value ? tr("检查中…") : h ? tr("重新检查") : tr("开始体检")}</button>
            </CardHeader>
            <ul class="health-checks" aria-busy={checking.value}>
              {checks.map((c) => (
                <li key={c.label}>
                  <span class={`health-indicator ${!h ? "" : c.ok ? "ok" : "failed"}`} aria-label={!h ? tr("未检查") : c.ok ? tr("正常") : tr("异常")}>{!h ? "–" : c.ok ? "✓" : "!"}</span>
                  <span class="health-copy"><span>{c.label}</span><span class="muted small">{h ? errorText(c.detail) : tr("等待检查")}</span></span>
                </li>
              ))}
            </ul>
          </section>
          <section class="card">
            <CardHeader title={tr("手动诊断")} />
            <div class="diagnostic-types" role="group" aria-label={tr("诊断类型")}>
              {DIAG_TYPES.map((t) => <button key={t.id} type="button" class="option" aria-pressed={kind.value === t.id} onClick={() => { editDiagnostic(); kind.value = t.id; }}>{t.label}</button>)}
            </div>
            <form class="diagnostic-form" onSubmit={(e) => { e.preventDefault(); void runDiag(); }}>
              {kind.value === "http" && <label class="field diagnostic-via">{tr("出口")}<select class="input" value={via.value} onChange={(e) => { editDiagnostic(); via.value = e.currentTarget.value; }}><option value="proxy">{tr("经代理")}</option><option value="direct">{tr("直连")}</option></select></label>}
              <label class="field">{tr("目标")}<input class="input" value={target.value} onInput={(e) => { editDiagnostic(); target.value = e.currentTarget.value; }} placeholder={tr("域名或 IP")} /></label>
              <button type="submit" class="btn primary" aria-busy={running.value} disabled={!target.value.trim()}>{running.value ? tr("运行中…") : tr("开始")}</button>
            </form>
            <pre class="diagnostic-output" aria-live="polite" aria-busy={running.value}>{serviceText(output.value) || tr("选择类型、填写目标后点“开始”。命令在路由器上执行，结果显示在这里")}</pre>
          </section>
        </div>
        <section class="card logs-card" aria-labelledby="combined-logs-title">
          <CardHeader title={tr("运行日志")} id="combined-logs-title">
            <div class="log-actions">
              <button type="button" class="btn" disabled={clearing.value} aria-busy={loadingLogs.value} onClick={() => { msg.value = ""; void loadLog(); }}>{tr("刷新")}</button>
              <button type="button" class="btn" disabled={clearing.value} onClick={() => clean()}>{tr("清空")}</button>
            </div>
          </CardHeader>
          <div class="segmented log-levels" role="tablist" aria-label={tr("日志级别")}>
            {LEVELS.map((option, index) => <button key={option.id} id={`log-level-${option.id}`} type="button" class="btn" role="tab" aria-selected={level.value === option.id} aria-controls="log-records" tabIndex={level.value === option.id ? 0 : -1} onClick={() => (level.value = option.id)} onKeyDown={(e) => {
              const next = e.key === "ArrowRight" ? (index + 1) % LEVELS.length : e.key === "ArrowLeft" ? (index + LEVELS.length - 1) % LEVELS.length : e.key === "Home" ? 0 : e.key === "End" ? LEVELS.length - 1 : -1;
              if (next < 0) return;
              e.preventDefault(); level.value = LEVELS[next].id;
              document.getElementById(`log-level-${LEVELS[next].id}`)?.focus();
            }}>{option.label}</button>)}
          </div>
          <div id="log-records" class="log-records" role="tabpanel" aria-labelledby={`log-level-${level.value}`} aria-busy={loadingLogs.value}>
            <pre class="log-textbox" role="textbox" aria-label={tr("运行日志")} aria-readonly="true" aria-multiline="true" tabIndex={0} onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "a") {
                e.preventDefault(); e.stopPropagation();
                const range = document.createRange();
                range.selectNodeContents(e.currentTarget);
                const selection = window.getSelection();
                selection?.removeAllRanges(); selection?.addRange(range);
              }
            }}>{records.length ? records.map((record, index) => {
              const label = LEVELS.find((item) => item.id === record.level)!.label;
              const time = record.time ? `${record.time} ` : "";
              const severity = `[${label}]`;
              const body = formatLogLine(record, label).slice(time.length + severity.length);
              return <span class="log-line" key={`${record.time}-${index}`}><span class="log-timestamp">{time}</span><span class={`log-severity ${record.level}`}>{severity}</span>{body}{index < records.length - 1 ? "\n" : ""}</span>;
            }) : <span class="log-placeholder">{loadingLogs.value ? tr("加载中…") : tr("没有日志")}</span>}</pre>
          </div>
        </section>
      </div>
    </main>
  );
}
