import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { useStore } from "../lib/store";
import { Disclosure, Notice, PageTitle, Row, Switch } from "../components/ui";
import { app } from "../lib/app";
import { call } from "../lib/rpc";
import { useAutosave } from "../components/AutosaveStatus";
import { configWrites, listWrites, ruleWrites } from "../lib/preferences";
import { invalidDomainLine, validDnsAddress } from "../lib/autosave";

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
  { value: "223.5.5.5", label: tr("阿里 DNS") },
  { value: "119.29.29.29", label: tr("腾讯 DNSPod") },
  { value: "https://223.5.5.5/dns-query", label: tr("阿里 DoH") },
  { value: "wan", label: tr("运营商 DNS（WAN 下发）") },
];

function DnsPicker({ label, value, presets, disabled, invalid, onPick, onBlur }: {
  label: string; value: string; presets: { value: string; label: string }[]; disabled: boolean; invalid: boolean;
  onPick: (v: string, immediate: boolean) => void; onBlur: () => void;
}) {
  const custom = !presets.some((p) => p.value === value);
  return (
    <label class="field control-field">
      {label}
      <select class="input" disabled={disabled} value={custom ? "__custom" : value} onChange={(e) => onPick(e.currentTarget.value === "__custom" ? "" : e.currentTarget.value, true)}>
        {presets.map((p) => <option key={p.value} value={p.value}>{`${p.label} · ${p.value}`}</option>)}
        <option value="__custom">{tr("自定义…")}</option>
      </select>
      {custom && (
        <input class="input mono" disabled={disabled} aria-invalid={invalid} value={value} placeholder={tr("例如 119.29.29.29 或 https://dns.google/dns-query")} onInput={(e) => onPick(e.currentTarget.value, false)} onBlur={onBlur} />
      )}
    </label>
  );
}

const dnsKeys = ["config.dns_server", "config.china_dns_server"] as const;
type DnsKey = typeof dnsKeys[number];
type ListKey = keyof typeof listWrites;

// Keep incomplete text across page navigation, without writing it to the router.
const textDrafts = new Map<string, string>();

export function Routing({ section, embedded = false }: { section: "rules" | "dns" | "custom"; embedded?: boolean }) {
  const msg = useStore({ text: "", tone: "error" as "error" | "ok" });
  const rules = useSignal<ServerRule[]>([]);
  const applies = useSignal(true);
  const lists = useStore({ direct_list: "", proxy_list: "" });
  const dns = useStore({ "config.dns_server": "", "config.china_dns_server": "" });
  const errors = useStore<Record<string, string>>({});
  const loadedSection = useSignal<"rules" | "dns" | "custom" | null>(null);
  useAutosave(configWrites, ruleWrites, listWrites.direct_list, listWrites.proxy_list);

  const tick = app.tick;
  useEffect(() => {
    let disposed = false;
    if (loadedSection.value !== section) loadedSection.value = null;
    const ruleRevision = ruleWrites.revision;
    const configRevision = configWrites.revision;
    const rulesAtRead = ruleWrites.value;
    const configAtRead = configWrites.value;
    const applyRules = (r: { rules: ServerRule[]; applies: boolean }) => {
      if (disposed || ruleRevision !== ruleWrites.revision) return;
      rules.value = (r.rules || []).map((rule) => ({ ...rule, enabled: ruleWrites.value?.[rule.key] ?? rulesAtRead?.[rule.key] ?? rule.enabled }));
      applies.value = r.applies;
    };
    const applyConfig = (r: { config: Record<string, string | null> }) => {
      if (disposed || configRevision !== configWrites.revision) return;
      for (const key of dnsKeys) {
        dns[key] = textDrafts.get(key) ?? configWrites.value?.[key] ?? configAtRead?.[key] ?? r.config[key] ?? "";
        if (textDrafts.has(key)) errors[key] = tr("DNS 地址格式不对");
      }
    };
    const loadList = (type: ListKey) => {
      const revision = listWrites[type].revision;
      const pendingAtRead = listWrites[type].value;
      const apply = (r: { content: string }) => {
        if (disposed || revision !== listWrites[type].revision) return;
        lists[type] = textDrafts.get(type) ?? listWrites[type].value ?? pendingAtRead ?? (r.content || "").trim();
        const invalid = invalidDomainLine(lists[type]);
        errors[type] = invalid ? tr("有一行不是域名：{0}", [invalid]) : "";
      };
      return call<{ content: string }>("acllist_read", { type }, { onUpdate: apply }).then(apply);
    };
    const reads = section === "rules" ? [
      call<{ rules: ServerRule[]; applies: boolean }>("rules_list", {}, { onUpdate: applyRules }).then(applyRules),
    ] : section === "custom" ? [loadList("direct_list"), loadList("proxy_list")] : [call<{ config: Record<string, string | null> }>("get_config", {}, { onUpdate: applyConfig }).then(applyConfig)];
    void Promise.all(reads).then(() => { if (!disposed) loadedSection.value = section; }).catch((e: Error) => { if (!disposed) msg.text = e.message; });
    return () => {
      disposed = true;
    };
  }, [section, tick]);

  const toggleRule = (key: string, enabled: boolean) => {
    rules.value = rules.value.map((r) => r.key === key ? { ...r, enabled } : r);
    ruleWrites.enqueue({ [key]: enabled });
  };

  const changeList = (type: ListKey, content: string) => {
    lists[type] = content;
    textDrafts.set(type, content);
    const invalid = invalidDomainLine(content);
    errors[type] = invalid ? tr("有一行不是域名：{0}", [invalid]) : "";
    if (invalid) listWrites[type].discardPending(() => null);
    else {
      textDrafts.delete(type);
      listWrites[type].enqueue(content, 650);
    }
  };

  const changeDns = (key: DnsKey, value: string, immediate: boolean) => {
    dns[key] = value;
    textDrafts.set(key, value);
    errors[key] = "";
    if (validDnsAddress(value.trim())) {
      textDrafts.delete(key);
      configWrites.enqueue({ [key]: value.trim() }, immediate ? 0 : 650);
    } else configWrites.discardPending((pending) => {
      const next = { ...pending };
      delete next[key];
      return Object.keys(next).length ? next : null;
    });
  };

  const blurDns = (key: DnsKey) => {
    if (!validDnsAddress(dns[key].trim())) errors[key] = tr("DNS 地址格式不对");
    else void configWrites.flush().catch(() => {});
  };
  const disabled = loadedSection.value !== section || !app.liveStatus;
  const Container = embedded ? "div" : "main";

  return (
    <Container class={embedded ? "embedded-settings" : "page"}>
      {!embedded && <PageTitle title={section === "rules" ? tr("分流规则") : section === "custom" ? tr("我的规则") : "DNS"} />}
      <Notice text={msg.text} tone={msg.tone} />

      {section === "rules" ? (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "2 1 480px" }}>
            {!applies.value && <div class="banner warn">{tr("当前是“全局代理”模式，这些规则只在“智能分流”模式下生效")}</div>}
            {applies.value && app.status?.mode === "dns" && <div class="banner">{tr("当前是“仅 DNS 保护”模式：规则只决定域名用国内 DNS 还是加密 DNS 解析，流量都直连")}</div>}
            {rules.value.length ? (
              <div>
                {rules.value.map((r) => (
                  <Row
                    key={r.key}
                    title={r.key}
                    desc={tr("{0} 条", [r.count ?? 0]) + (r.stale ? tr(" · 使用上次的规则") : "") + (!r.server_enabled ? tr(" · 服务端已停用") : "")}
                  >
                    <span class="pill" style={{ color: r.policy === "DIRECT" ? "var(--good)" : "var(--info)" }}>{r.policy === "DIRECT" ? tr("直连") : tr("代理")}</span>
                    <Switch label={tr("启用 {0}", [r.key])} checked={r.enabled} disabled={disabled || !r.server_enabled} onToggle={(on) => toggleRule(r.key, on)} />
                  </Row>
                ))}
              </div>
            ) : (
              <div class="empty" style={{ minHeight: "160px" }}>
                <span class="muted">{tr("暂无规则")}</span>
              </div>
            )}
          </section>
        </div>
      ) : section === "custom" ? (
        <div class="row">
          <section class="card" style={{ flex: "1 1 320px" }}>
            {!embedded && <p class="muted small" style={{ margin: 0 }}>{tr("每行一个域名或关键词，比服务端规则优先")}</p>}
            <div class="domain-lists">
            {(["direct_list", "proxy_list"] as const).map((t) => (
              <Disclosure key={t} title={t === "direct_list" ? tr("直连规则") : tr("代理规则")}>
                <textarea
                  class="input mono"
                  aria-label={t === "direct_list" ? tr("直连规则") : tr("代理规则")}
                  rows={3}
                  style={{ padding: "12px 14px", minHeight: "90px", fontSize: "13px", resize: "vertical" }}
                  placeholder={t === "direct_list" ? tr("例如：\ncompany.com\nlocal-bank") : tr("例如：\nopenai.com\ngithub.com")}
                  value={lists[t]}
                  disabled={disabled}
                  aria-invalid={!!errors[t]}
                  onInput={(e) => changeList(t, e.currentTarget.value)}
                  onBlur={() => { void listWrites[t].flush().catch(() => {}); }}
                />
                {errors[t] && <Notice text={errors[t]} />}
              </Disclosure>
            ))}
            </div>
          </section>
        </div>
      ) : (
        <div class="row" style={{ alignItems: "flex-start" }}>
          <section class="card" style={{ flex: "1 1 420px" }}>
            {!embedded && <h2>{tr("DNS 服务器")}</h2>}
            <DnsPicker label={tr("境外域名（经代理查询）")} value={dns["config.dns_server"]} presets={PROXY_DNS} disabled={disabled} invalid={!!errors["config.dns_server"]} onPick={(v, immediate) => changeDns("config.dns_server", v, immediate)} onBlur={() => blurDns("config.dns_server")} />
            <Notice text={errors["config.dns_server"] || ""} />
            <DnsPicker label={tr("国内域名与直连规则（直接查询）")} value={dns["config.china_dns_server"]} presets={CN_DNS} disabled={disabled} invalid={!!errors["config.china_dns_server"]} onPick={(v, immediate) => changeDns("config.china_dns_server", v, immediate)} onBlur={() => blurDns("config.china_dns_server")} />
            <Notice text={errors["config.china_dns_server"] || ""} />

          </section>
        </div>
      )}
    </Container>
  );
}
