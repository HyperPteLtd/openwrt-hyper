import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { Notice, PageTitle, Segmented } from "../components/ui";
import { call } from "../lib/rpc";

interface Health {
  service?: { ok: boolean };
  node?: { delay?: number; message?: string } | null;
  dns?: { ok: boolean };
  direct?: { ok: boolean; ms?: number | null };
  proxy?: { ok: boolean; ms?: number | null };
}

const DIAG_TYPES = [
  { id: "ping", label: "Ping" },
  { id: "traceroute", label: "路由追踪" },
  { id: "nslookup", label: "DNS 查询" },
  { id: "http", label: "HTTP 测试" },
];

const LEVEL_COLOR: Record<string, string> = { INFO: "var(--info)", WARN: "#ffb547", ERROR: "var(--danger-text)", FATAL: "var(--danger-text)" };

export function Logs() {
  const tab = useSignal<"diag" | "log">("diag");
  const msg = useSignal("");

  /* diagnostics */
  const health = useSignal<Health | null>(null);
  const checking = useSignal(false);
  const kind = useSignal("ping");
  const target = useSignal("www.google.com");
  const via = useSignal("proxy");
  const output = useSignal("");
  const running = useSignal(false);

  /* logs */
  const logType = useSignal<"sing-box-c" | "hyper">("sing-box-c");
  const level = useSignal<"all" | "warn" | "error">("all");
  const content = useSignal("");

  const loadLog = async () => {
    try {
      const r = await call<{ content?: string; error?: string }>("logs", { type: logType.value, lines: 300 });
      content.value = r.content || "";
    } catch (e) {
      msg.value = (e as Error).message;
    }
  };

  const isLog = tab.value === "log";
  const type = logType.value;
  useEffect(() => {
    if (isLog) loadLog();
  }, [isLog, type]);

  const runHealth = async () => {
    checking.value = true;
    try {
      health.value = await call<Health>("health_check");
    } catch (e) {
      msg.value = (e as Error).message;
    }
    checking.value = false;
  };

  const runDiag = async () => {
    running.value = true;
    output.value = "";
    try {
      const r = await call<{ result: boolean; output?: string; error?: string }>("diag", { type: kind.value, target: target.value.trim(), via: via.value });
      output.value = r.error === "illegal target" ? "目标只能是域名或 IP 地址。" : r.output || r.error || "";
    } catch (e) {
      output.value = (e as Error).message;
    }
    running.value = false;
  };

  const clean = async () => {
    await call("log_clean", { type: logType.value });
    await loadLog();
  };

  const h = health.value;
  const isChecking = checking.value;
  const checks = [
    { label: "服务进程", ok: h?.service?.ok, detail: h ? (h.service?.ok ? "sing-box 运行中" : "sing-box 没有运行") : "" },
    { label: "节点连通", ok: !!h?.node?.delay, detail: h ? (h.node?.delay ? `当前节点 ${h.node.delay} ms` : h.node?.message || "当前节点无法连通") : "" },
    { label: "DNS 解析", ok: h?.dns?.ok, detail: h ? (h.dns?.ok ? "解析正常" : "解析失败") : "" },
    { label: "国内网站直连", ok: h?.direct?.ok, detail: h ? (h.direct?.ok ? `www.baidu.com · ${h.direct.ms} ms` : "无法访问") : "" },
    { label: "境外网站经代理", ok: h?.proxy?.ok, detail: h ? (h.proxy?.ok ? `www.google.com · ${h.proxy.ms} ms` : "无法访问") : "" },
  ];

  const lines = content.value
    .split("\n")
    .filter((l) => l.trim())
    .filter((l) => level.value === "all" || (level.value === "warn" ? /\b(WARN|ERROR|FATAL)\b/.test(l) : /\b(ERROR|FATAL)\b/.test(l)));

  return (
    <main class="page">
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: "16px" }}>
        <PageTitle title="日志与诊断" sub="连不上或变慢时，先跑一次体检，再看日志。" />
        <Segmented
          label="日志与诊断"
          value={tab.value}
          options={[{ id: "diag", label: "网络诊断" }, { id: "log", label: "运行日志" }]}
          onChange={(id) => (tab.value = id as "diag" | "log")}
        />
      </div>
      <Notice text={msg.value} />

      {tab.value === "diag" ? (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "1 1 320px" }}>
            <div class="card-head">
              <h2>一键体检</h2>
              <button type="button" class="btn primary" disabled={checking.value} onClick={() => runHealth()}>
                {isChecking ? "检查中…" : h ? "重新检查" : "开始体检"}
              </button>
            </div>
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {checks.map((c) => (
                <li key={c.label} style={{ display: "flex", alignItems: "center", gap: "12px", minHeight: "56px", borderTop: "1px solid var(--line)" }}>
                  <span
                    aria-label={!h ? "未检查" : c.ok ? "正常" : "异常"}
                    style={{
                      width: "26px", height: "26px", borderRadius: "13px", flexShrink: 0, display: "inline-flex", alignItems: "center", justifyContent: "center", fontWeight: 700,
                      background: !h ? "var(--control)" : c.ok ? "var(--accent)" : "var(--danger)",
                      color: !h ? "var(--muted)" : c.ok ? "var(--accent-ink)" : "#fff",
                    }}
                  >
                    {!h ? "–" : c.ok ? "✓" : "!"}
                  </span>
                  <span style={{ display: "flex", flexDirection: "column", gap: "3px" }}>
                    <span>{c.label}</span>
                    <span class="muted small">{h ? c.detail : "等待检查"}</span>
                  </span>
                </li>
              ))}
            </ul>
          </section>

          <section class="card" style={{ flex: "2 1 460px" }}>
            <h2>手动诊断</h2>
            <div role="group" aria-label="诊断类型" style={{ display: "flex", flexWrap: "wrap", gap: "6px" }}>
              {DIAG_TYPES.map((t) => (
                <button key={t.id} type="button" class="option" aria-pressed={kind.value === t.id} style={{ minHeight: "40px", padding: "0 16px" }} onClick={() => (kind.value = t.id)}>
                  {t.label}
                </button>
              ))}
            </div>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                runDiag();
              }}
              style={{ display: "flex", flexWrap: "wrap", gap: "10px", alignItems: "flex-end" }}>
              <label class="field" style={{ flex: "1 1 240px" }}>
                目标
                <input class="input" value={target.value} onInput={(e) => (target.value = e.currentTarget.value)} placeholder="域名或 IP" />
              </label>
              {kind.value === "http" && (
                <label class="field" style={{ flex: "0 1 160px" }}>
                  出口
                  <select class="input" onChange={(e) => (via.value = e.currentTarget.value)}>
                    <option value="proxy" selected={via.value === "proxy"}>经代理</option>
                    <option value="direct" selected={via.value === "direct"}>直连</option>
                  </select>
                </label>
              )}
              <button type="submit" class="btn primary" disabled={running.value || !target.value.trim()}>
                {running.value ? "运行中…" : "开始"}
              </button>
            </form>
            <pre class="mono" style={{ margin: 0, minHeight: "200px", padding: "16px", borderRadius: "14px", background: "#0e0e0f", border: "1px solid var(--control)", fontSize: "12.5px", lineHeight: 1.7, whiteSpace: "pre-wrap", overflowX: "auto", color: "var(--text-2)" }}>
              {output.value || "选择类型、填写目标后点“开始”。命令在路由器上执行，结果显示在这里。"}
            </pre>
          </section>
        </div>
      ) : (
        <section class="card">
          <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "10px" }}>
            <Segmented
              label="日志来源"
              value={logType.value}
              options={[{ id: "sing-box-c", label: "sing-box" }, { id: "hyper", label: "Hyper" }]}
              onChange={(id) => (logType.value = id as "sing-box-c" | "hyper")}
            />
            <Segmented
              label="日志级别"
              value={level.value}
              options={[{ id: "all", label: "全部" }, { id: "warn", label: "警告及以上" }, { id: "error", label: "仅错误" }]}
              onChange={(id) => (level.value = id as "all" | "warn" | "error")}
            />
            <span style={{ flexGrow: 1 }} />
            <button type="button" class="btn" onClick={() => loadLog()}>刷新</button>
            <button type="button" class="btn" onClick={() => clean()}>清空</button>
          </div>
          <ol class="mono" style={{ listStyle: "none", margin: 0, padding: "12px 16px", minHeight: "420px", borderRadius: "14px", background: "#0e0e0f", border: "1px solid var(--control)", fontSize: "12.5px", lineHeight: 1.9, overflowX: "auto" }}>
            {lines.length ? (
              lines.map((l, i) => {
                const lv = (l.match(/\b(INFO|WARN|ERROR|FATAL|DEBUG|TRACE)\b/) || [])[1];
                return (
                  <li key={i} style={{ whiteSpace: "pre", color: lv ? LEVEL_COLOR[lv] || "var(--text-2)" : "var(--text-2)" }}>
                    {l}
                  </li>
                );
              })
            ) : (
              <li class="muted">没有日志。</li>
            )}
          </ol>
        </section>
      )}
    </main>
  );
}
