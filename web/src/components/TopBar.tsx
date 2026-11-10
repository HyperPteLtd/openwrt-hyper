import { app, ROUTES, tabOf } from "../lib/app";

export const Logo = ({ size = 28 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 26 26" aria-hidden="true">
    <path d="M9 2h15l-6 6H3z" fill="var(--accent)" />
    <path d="M9 10h15l-6 6H3z" fill="var(--accent)" />
    <path d="M9 18h11l-6 6H3z" fill="var(--accent)" />
  </svg>
);

export function TopBar() {
  const s = app.status;
  const tone = !s ? "var(--muted)" : s.health?.state === "failopen" ? "var(--warn)" : s.running ? "var(--accent)" : s.enabled ? "var(--danger-text)" : "var(--muted)";
  const tab = tabOf(app.route);
  const label = !s ? "加载中" : s.health?.state === "failopen" ? "已临时直连" : s.running ? "运行中" : s.enabled ? "连接失败" : "已停止";

  return (
    <header class="topbar">
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "12px 16px" }}>
        <Logo />
        <h1 style={{ margin: 0, fontSize: "24px", fontWeight: 600 }}>Hyper</h1>
        <span class="pill" style={{ color: tone }}>
          <span class="dot" />
          {label}
        </span>
        {s?.update?.available && (
          <a href="#/settings" class="pill" style={{ color: "var(--accent)" }}>
            有新版本 {s.update.latest}
          </a>
        )}
        <span style={{ flexGrow: 1 }} />
        {s?.signed_in && s.email && (
          <a href="#/account" class="pill" style={{ minHeight: "36px", padding: "0 14px" }}>
            {s.email}
          </a>
        )}
      </div>
      <nav aria-label="Hyper 页面" style={{ display: "flex", gap: "4px", overflowX: "auto", borderBottom: "1px solid var(--control)" }}>
        {ROUTES.map((r) => (
          <a
            key={r.id}
            href={`#/${r.id}`}
            aria-current={tab === r.id ? "page" : undefined}
            style={{
              flexShrink: 0,
              display: "inline-flex",
              alignItems: "center",
              minHeight: "44px",
              padding: "0 14px",
              fontSize: "15px",
              whiteSpace: "nowrap",
              borderBottom: `2px solid ${tab === r.id ? "var(--accent)" : "transparent"}`,
              color: tab === r.id ? "var(--text)" : "#b4b4ba",
              fontWeight: tab === r.id ? 600 : 400,
            }}
          >
            {r.label}
          </a>
        ))}
      </nav>
    </header>
  );
}
