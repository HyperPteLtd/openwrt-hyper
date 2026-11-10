import { encode } from "uqr";

/** Scannable QR code as one SVG path (dark modules on a white quiet zone). */
export function QrCode({ value, size = 220, label }: { value: string; size?: number; label: string }) {
  let d = "";
  let modules = 0;
  try {
    const qr = encode(value, { ecc: "M", border: 0 });
    modules = qr.size;
    qr.data.forEach((row, y) => {
      for (let x = 0; x < row.length; x++) {
        if (!row[x]) continue;
        let run = 1;
        while (x + run < row.length && row[x + run]) run++;
        d += `M${x} ${y}h${run}v1h-${run}z`;
        x += run - 1;
      }
    });
  } catch {
    modules = 0;
  }

  if (!modules) return <span class="muted small">内容太长，无法生成二维码。</span>;

  const pad = 4;
  return (
    <span style={{ display: "inline-flex", padding: "12px", borderRadius: "16px", background: "#fff", lineHeight: 0 }}>
      <svg
        role="img"
        aria-label={label}
        width={size}
        height={size}
        viewBox={`${-pad} ${-pad} ${modules + pad * 2} ${modules + pad * 2}`}
        shape-rendering="crispEdges"
        style={{ display: "block", maxWidth: "100%", height: "auto" }}
      >
        <rect x={-pad} y={-pad} width={modules + pad * 2} height={modules + pad * 2} fill="#fff" />
        <path d={d} fill="#000" />
      </svg>
    </span>
  );
}
