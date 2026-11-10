import type { ComponentChildren } from "preact";

/** Accessible on/off switch (role="switch") */
export function Switch({ checked, label, disabled, onToggle }: { checked: boolean; label: string; disabled?: boolean; onToggle: (next: boolean) => void }) {
  return (
    <button type="button" role="switch" class="switch" aria-checked={checked} aria-label={label} disabled={disabled} onClick={() => onToggle(!checked)}>
      <span />
    </button>
  );
}

/** A settings row: title + description on the left, a control on the right */
export function Row({ title, desc, children }: { title: string; desc?: string; children?: ComponentChildren }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: "12px", minHeight: "64px", borderTop: "1px solid var(--line)" }}>
      <span style={{ flexGrow: 1, display: "flex", flexDirection: "column", gap: "4px" }}>
        <span style={{ fontSize: "15px" }}>{title}</span>
        {desc && <span class="muted small">{desc}</span>}
      </span>
      {children}
    </div>
  );
}

/** Segmented control / tab strip */
export function Segmented({ label, value, options, onChange }: { label: string; value: string; options: { id: string; label: string }[]; onChange: (id: string) => void }) {
  return (
    <div role="tablist" aria-label={label} style={{ display: "flex", flexWrap: "wrap", gap: "4px", padding: "4px", borderRadius: "24px", background: "var(--card)", alignSelf: "flex-start" }}>
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="tab"
          aria-selected={value === o.id}
          onClick={() => onChange(o.id)}
          class="btn"
          style={{ minHeight: "40px", padding: "0 18px", background: value === o.id ? "var(--control)" : "transparent", color: value === o.id ? "var(--text)" : "#b4b4ba" }}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function PageTitle({ title, sub }: { title: string; sub?: string }) {
  return (
    /* basis 0 + min-width: a 280px basis would become a 280px *height* in a column layout */
    <div style={{ flex: "1 1 0", minWidth: "min(280px, 100%)", display: "flex", flexDirection: "column", gap: "6px" }}>
      <h2 style={{ margin: 0, fontSize: "26px", fontWeight: 600 }}>{title}</h2>
      {sub && <p class="muted" style={{ margin: 0 }}>{sub}</p>}
    </div>
  );
}

/** Inline message: error (default), warn or ok */
export function Notice({ text, tone = "error" }: { text: string; tone?: "error" | "warn" | "ok" }) {
  if (!text) return null;
  return (
    <div
      class={`banner ${tone === "warn" ? "warn" : ""}`}
      role={tone === "error" ? "alert" : "status"}
      style={tone === "ok" ? { background: "#141a0a", borderColor: "#3e5214", color: "#dde8c8" } : undefined}
    >
      {text}
    </div>
  );
}
