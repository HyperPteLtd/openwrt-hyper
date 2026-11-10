import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { useStore } from "../lib/store";
import { Notice, PageTitle, Row, Segmented, Switch } from "../components/ui";
import { app } from "../lib/app";
import { call } from "../lib/rpc";
import type { Result } from "../lib/types";

interface ServerRule {
  key: string;
  policy: "DIRECT" | "PROXY";
  enabled: boolean;
  server_enabled: boolean;
  count?: number;
  source?: string;
  stale?: boolean;
}

/* Same defaults and presets as the Hyper app (crates/core/src/dns.rs) */
const PROXY_DNS = [
  { value: "https://1.1.1.1/dns-query", label: "Cloudflare DoH" },
  { value: "https://8.8.8.8/dns-query", label: "Google DoH" },
  { value: "tls://1.1.1.1", label: "Cloudflare DoT" },
  { value: "1.1.1.1", label: "Cloudflare UDP" },
  { value: "8.8.8.8", label: "Google UDP" },
];
const CN_DNS = [
  { value: "223.5.5.5", label: "阿里 DNS" },
  { value: "119.29.29.29", label: "腾讯 DNSPod" },
  { value: "https://223.5.5.5/dns-query", label: "阿里 DoH" },
  { value: "wan", label: "运营商 DNS（WAN 下发）" },
];

function DnsPicker({ label, value, presets, onPick }: { label: string; value: string; presets: { value: string; label: string }[]; onPick: (v: string) => void }) {
  const custom = !presets.some((p) => p.value === value);
  return (
    <label class="field">
      {label}
      <select class="input" onChange={(e) => onPick(e.currentTarget.value === "__custom" ? "" : e.currentTarget.value)}>
        {presets.map((p) => (
          <option key={p.value} value={p.value} selected={!custom && p.value === value}>{`${p.label} · ${p.value}`}</option>
        ))}
        <option value="__custom" selected={custom}>自定义…</option>
      </select>
      {custom && (
        <input class="input mono" value={value} placeholder="例如 119.29.29.29 或 https://dns.google/dns-query" onInput={(e) => onPick(e.currentTarget.value.trim())} />
      )}
    </label>
  );
}

export function Routing() {
  const section = useSignal<"rules" | "dns">("rules");
  const msg = useStore({ text: "", tone: "error" as "error" | "ok" });

  /* server rules + custom lists */
  const rules = useSignal<ServerRule[]>([]);
  const applies = useSignal(true);
  const lists = useStore({ direct_list: "", proxy_list: "" });
  const savedLists = useStore({ direct_list: "", proxy_list: "" });

  /* dns */
  const dns = useStore({ "config.dns_server": "", "config.china_dns_server": "" });
  const savedDns = useStore({ "config.dns_server": "", "config.china_dns_server": "" });
  const busy = useSignal("");

  useEffect(() => {
    (async () => {
      try {
        const [r, d, p, c] = await Promise.all([
          call<{ rules: ServerRule[]; applies: boolean }>("rules_list"),
          call<{ content: string }>("acllist_read", { type: "direct_list" }),
          call<{ content: string }>("acllist_read", { type: "proxy_list" }),
          call<{ config: Record<string, string | null> }>("get_config"),
        ]);
        rules.value = r.rules || [];
        applies.value = r.applies;
        lists.direct_list = savedLists.direct_list = (d.content || "").trim();
        lists.proxy_list = savedLists.proxy_list = (p.content || "").trim();
        dns["config.dns_server"] = savedDns["config.dns_server"] = c.config["config.dns_server"] || "";
        dns["config.china_dns_server"] = savedDns["config.china_dns_server"] = c.config["config.china_dns_server"] || "";
      } catch (e) {
        msg.text = (e as Error).message;
      }
    })();
  }, []);

  const toggleRule = async (key: string, enabled: boolean) => {
    rules.value = rules.value.map((r) => (r.key === key ? { ...r, enabled } : r));
    try {
      const r = await call<Result>("set_rule_enabled", { key, enabled });
      if (!r.result) throw new Error(r.error || "保存失败");
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
      rules.value = rules.value.map((r) => (r.key === key ? { ...r, enabled: !enabled } : r));
    }
  };

  const saveList = async (type: "direct_list" | "proxy_list") => {
    busy.value = type;
    try {
      const r = await call<Result & { count?: number }>("acllist_write", { type, content: lists[type] });
      if (r.result) {
        savedLists[type] = lists[type];
        msg.text = `已保存 ${r.count ?? 0} 条。`;
        msg.tone = "ok";
      } else {
        msg.text = r.error?.startsWith("illegal entry") ? `有一行不是域名：${r.error.slice(15)}` : r.error || "保存失败";
        msg.tone = "error";
      }
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
    busy.value = "";
  };

  const saveDns = async () => {
    busy.value = "dns";
    try {
      const r = await call<Result>("set_config", { values: { ...dns } });
      if (r.result) {
        savedDns["config.dns_server"] = dns["config.dns_server"];
        savedDns["config.china_dns_server"] = dns["config.china_dns_server"];
        msg.text = "DNS 已保存。";
        msg.tone = "ok";
      } else {
        msg.text = r.error?.startsWith("illegal value") ? "DNS 地址格式不对。" : r.error || "保存失败";
        msg.tone = "error";
      }
    } catch (e) {
      msg.text = (e as Error).message;
      msg.tone = "error";
    }
    busy.value = "";
    app.tick++;
  };

  const dnsDirty = dns["config.dns_server"] !== savedDns["config.dns_server"] || dns["config.china_dns_server"] !== savedDns["config.china_dns_server"];
  const isBusy = !!busy.value;

  return (
    <main class="page">
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: "16px" }}>
        <PageTitle title="分流与 DNS" sub="决定哪些网站直连、哪些走代理，以及路由器怎么解析域名。保存后服务会自动重载。" />
        <Segmented label="分流与 DNS" value={section.value} options={[{ id: "rules", label: "分流规则" }, { id: "dns", label: "DNS" }]} onChange={(id) => (section.value = id as "rules" | "dns")} />
      </div>
      <Notice text={msg.text} tone={msg.tone} />

      {section.value === "rules" ? (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "2 1 480px" }}>
            <div class="card-head">
              <h2>服务端规则</h2>
              <span class="muted small">随节点一起同步，按顺序匹配</span>
            </div>
            {!applies.value && <div class="banner warn">当前是“全局代理”模式，这些规则只在“智能分流”模式下生效。</div>}
            {applies.value && app.status?.mode === "dns" && <div class="banner">当前是“仅 DNS 保护”模式：规则只决定域名用国内 DNS 还是加密 DNS 解析，流量都直连。</div>}
            {rules.value.length ? (
              <div>
                {rules.value.map((r) => (
                  <Row
                    key={r.key}
                    title={r.key}
                    desc={`${r.policy === "DIRECT" ? "直连" : "代理"} · ${r.count ?? 0} 条${r.source === "url" ? " · 远程列表" : ""}${r.stale ? " · 本次下载失败，用的是上次的副本" : ""}${!r.server_enabled ? " · 服务端已停用" : ""}`}
                  >
                    <span class="pill" style={{ color: r.policy === "DIRECT" ? "var(--good)" : "var(--info)" }}>{r.policy === "DIRECT" ? "直连" : "代理"}</span>
                    <Switch label={`启用 ${r.key}`} checked={r.enabled} disabled={!r.server_enabled} onToggle={(on) => toggleRule(r.key, on)} />
                  </Row>
                ))}
              </div>
            ) : (
              <div class="empty" style={{ minHeight: "160px" }}>
                <span class="muted">还没有服务端规则。登录后同步节点时会一起下载。</span>
              </div>
            )}
          </section>

          <section class="card" style={{ flex: "1 1 320px" }}>
            <h2>我的规则</h2>
            <p class="muted small" style={{ margin: 0 }}>每行一个域名或关键词，比服务端规则优先。</p>
            {(["direct_list", "proxy_list"] as const).map((t) => (
              <label key={t} class="field">
                {t === "direct_list" ? "总是直连" : "总是走代理"}
                <textarea
                  class="input mono"
                  rows={5}
                  style={{ padding: "12px 14px", minHeight: "120px", fontSize: "13px", resize: "vertical" }}
                  placeholder={t === "direct_list" ? "例如：\ncompany.com\nlocal-bank" : "例如：\nopenai.com\ngithub.com"}
                  value={lists[t]}
                  onInput={(e) => (lists[t] = e.currentTarget.value)}
                />
                <button type="button" class="btn" style={{ alignSelf: "flex-end" }} disabled={lists[t] === savedLists[t] || !!busy.value} onClick={() => saveList(t)}>
                  {busy.value === t ? "保存中…" : "保存"}
                </button>
              </label>
            ))}
          </section>
        </div>
      ) : (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "1 1 420px" }}>
            <h2>DNS 服务器</h2>
            <DnsPicker label="境外域名（经代理查询）" value={dns["config.dns_server"]} presets={PROXY_DNS} onPick={(v) => (dns["config.dns_server"] = v)} />
            <DnsPicker label="国内域名与直连规则（直接查询）" value={dns["config.china_dns_server"]} presets={CN_DNS} onPick={(v) => (dns["config.china_dns_server"] = v)} />
            <p class="muted small" style={{ margin: 0 }}>默认值和 Hyper App 一致：境外用加密的 Cloudflare DoH，国内用阿里 DNS。</p>
            <button type="button" class="btn primary" style={{ alignSelf: "flex-start" }} disabled={!dnsDirty || isBusy || !dns["config.dns_server"] || !dns["config.china_dns_server"]} onClick={() => saveDns()}>
              {busy.value === "dns" ? "保存中…" : "保存并应用"}
            </button>
          </section>
        </div>
      )}
    </main>
  );
}
