import type { ComponentChildren } from "preact";
import { serviceText } from "../lib/display";

/** Shared surfaces keep page spacing and headings consistent. */
export function Card({ children, className = "", label, id }: { children: ComponentChildren; className?: string; label?: string; id?: string }) {
  return <section id={id} class={`card ${className}`} aria-label={label}>{children}</section>;
}

export function CardHeader({ title, desc, children, id }: { title: string; desc?: string; children?: ComponentChildren; id?: string }) {
  return <div class="card-header"><div class="card-heading"><h2 id={id}>{title}</h2>{desc && <p class="muted small">{desc}</p>}</div>{children && <div class="card-actions">{children}</div>}</div>;
}

/** Native disclosure keeps keyboard and expanded-state behavior consistent. */
export function Disclosure({ title, children }: { title: string; children: ComponentChildren }) {
  return <details class="disclosure"><summary>{title}</summary><div class="disclosure-content">{children}</div></details>;
}

export function Toolbar({ children }: { children: ComponentChildren }) {
  return <div class="toolbar">{children}</div>;
}

/** Accessible on/off switch (role="switch") */
export function Switch({ checked, label, disabled, onToggle }: { checked: boolean; label: string; disabled?: boolean; onToggle: (next: boolean) => void }) {
  return <button type="button" role="switch" class="switch" aria-checked={checked} aria-label={label} disabled={disabled} onClick={() => onToggle(!checked)}><span /></button>;
}

/** A settings row: title + description on the left, a control on the right. */
export function Row({ title, desc, children }: { title: string; desc?: string; children?: ComponentChildren }) {
  return <div class="settings-row"><span class="settings-row-copy"><span class="settings-row-title">{title}</span>{desc && <span class="muted small">{desc}</span>}</span><div class="settings-row-control">{children}</div></div>;
}

/** Selection control for workflows, never used for secondary page navigation. */
export function Segmented({ label, value, options, onChange }: { label: string; value: string; options: { id: string; label: string }[]; onChange: (id: string) => void }) {
  return <div role="tablist" aria-label={label} class="segmented">{options.map((o) => <button key={o.id} type="button" role="tab" aria-selected={value === o.id} onClick={() => onChange(o.id)} class="btn">{o.label}</button>)}</div>;
}

export function PageTitle({ title, sub, children }: { title: string; sub?: string; children?: ComponentChildren }) {
  return <div class="page-header"><div class="page-heading"><h2>{title}</h2>{sub && <p class="muted">{sub}</p>}</div>{children && <div class="toolbar">{children}</div>}</div>;
}

/** Inline message: error (default), warn or ok. */
export function Notice({ text, tone = "error" }: { text: string; tone?: "error" | "warn" | "ok" }) {
  if (!text) return null;
  return <div class={`banner ${tone}`} role={tone === "error" ? "alert" : "status"}>{serviceText(text)}</div>;
}
