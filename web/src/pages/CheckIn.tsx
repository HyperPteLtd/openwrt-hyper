import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { Notice } from "../components/ui";
import { checkIn, checkinError, checkinStatus, dateKey, monthRange, monthView, streak, type DayCell } from "../lib/checkin";

const WEEKDAYS = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];

const STATE_LABEL: Record<DayCell["state"], string> = {
  checked: "已签到",
  "today-checked": "今天，已签到",
  today: "今天，未签到",
  missed: "未签到",
  future: "",
};

const Tick = () => (
  <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true" style={{ display: "block" }}>
    <circle cx="11" cy="11" r="11" fill="var(--accent)" />
    <path d="M6.5 11.2l3 3 6-6.4" fill="none" stroke="#111" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
);

/** Daily check-in (Figma M21). Kept on the router per account; PPanel has no check-in API. */
export function CheckIn() {
  const dates = useSignal<string[]>([]);
  const loaded = useSignal(false);
  const busy = useSignal(false);
  const msg = useSignal<{ text: string; tone: "error" | "ok" }>({ text: "", tone: "error" });
  const now = new Date();
  const view = useSignal<[number, number]>([now.getFullYear(), now.getMonth()]);

  useEffect(() => {
    (async () => {
      try {
        const r = await checkinStatus();
        if (r.result) dates.value = r.dates ?? [];
        else msg.value = { text: checkinError(r), tone: "error" };
      } catch (e) {
        msg.value = { text: (e as Error).message, tone: "error" };
      }
      loaded.value = true;
    })();
  }, []);

  const today = dateKey(new Date());
  const checked = new Set(dates.value);
  const doneToday = checked.has(today);
  const run = streak(checked, today);
  const [vy, vm] = view.value;
  const month = monthView(vy, vm, checked, today);
  const range = monthRange(dates.value, today);
  const atFirst = vy * 12 + vm <= range.first[0] * 12 + range.first[1];
  const atLast = vy * 12 + vm >= range.last[0] * 12 + range.last[1];
  const isLoaded = loaded.value;
  const isBusy = busy.value;
  const notice = msg.value;
  const total = dates.value.length;
  const buttonDisabled = !isLoaded || doneToday || isBusy;
  const buttonLabel = doneToday ? "今日已签到" : isBusy ? "签到中…" : "立即签到";

  const go = (delta: number) => {
    const [y, m] = view.value;
    const d = new Date(y, m + delta, 1, 12);
    view.value = [d.getFullYear(), d.getMonth()];
  };

  const submit = async () => {
    busy.value = true;
    msg.value = { text: "", tone: "error" };
    const d = new Date();
    try {
      const r = await checkIn(dateKey(d));
      if (r.result) {
        dates.value = r.dates ?? [...dates.value, dateKey(d)];
        view.value = [d.getFullYear(), d.getMonth()];
        const n = streak(new Set(dates.value), dateKey(d));
        msg.value = { text: n > 1 ? `签到成功，已连续签到 ${n} 天。` : "签到成功。", tone: "ok" };
      } else msg.value = { text: checkinError(r), tone: "error" };
    } catch (e) {
      msg.value = { text: (e as Error).message, tone: "error" };
    }
    busy.value = false;
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "20px", width: "100%", maxWidth: "760px" }}>
      <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
        <div style={{ flex: "1 1 0", minWidth: 0, display: "flex", flexDirection: "column", gap: "6px" }}>
          <h2 style={{ margin: 0, fontSize: "26px", fontWeight: 600 }}>每日签到</h2>
          <p class="muted" style={{ margin: 0 }}>每天来看看，记录连续签到的天数。</p>
        </div>
        <span
          aria-hidden="true"
          style={{ flexShrink: 0, width: "56px", height: "56px", borderRadius: "18px", background: "var(--card-2)", display: "inline-flex", alignItems: "center", justifyContent: "center", boxShadow: "inset 0 0 24px rgba(195,255,46,0.18)" }}
        >
          <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="var(--accent)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3.5" y="5" width="17" height="15.5" rx="3" />
            <path d="M8 3v4M16 3v4M3.5 10h17M9 15l2 2 4-4" />
          </svg>
        </span>
      </div>

      <Notice text={notice.text} tone={notice.tone} />

      <section class="card" aria-busy={!isLoaded} style={{ gap: "18px" }}>
        <div class="card-head" style={{ flexWrap: "wrap" }}>
          <div style={{ display: "flex", alignItems: "center", gap: "4px" }}>
            <button type="button" class="btn ghost" style={{ minHeight: "36px", width: "36px", padding: 0 }} aria-label="上个月" disabled={atFirst} onClick={() => go(-1)}>
              ‹
            </button>
            <h3 aria-live="polite" style={{ margin: 0, fontSize: "16px", fontWeight: 600, minWidth: "104px", textAlign: "center", fontVariantNumeric: "tabular-nums" }}>
              {month.year} 年 {month.month + 1} 月
            </h3>
            <button type="button" class="btn ghost" style={{ minHeight: "36px", width: "36px", padding: 0 }} aria-label="下个月" disabled={atLast} onClick={() => go(1)}>
              ›
            </button>
          </div>
          <span class="muted small">本月已签到 {month.count} 天</span>
        </div>

        <div aria-hidden="true" style={{ display: "grid", gridTemplateColumns: "repeat(7, minmax(0, 1fr))", gap: "6px" }}>
          {WEEKDAYS.map((w) => (
            <span key={w} class="small" style={{ textAlign: "center", color: "var(--text-2)" }}>
              {w}
            </span>
          ))}
        </div>

        <ol aria-label={`${month.year} 年 ${month.month + 1} 月签到记录`} style={{ listStyle: "none", margin: 0, padding: 0, display: "grid", gridTemplateColumns: "repeat(7, minmax(0, 1fr))", gap: "6px" }}>
          {month.days.map((d, i) => {
            const isToday = d.state === "today" || d.state === "today-checked";
            const isChecked = d.state === "checked" || d.state === "today-checked";
            return (
              <li
                key={d.key}
                aria-label={`${month.month + 1} 月 ${d.day} 日${STATE_LABEL[d.state] ? "，" + STATE_LABEL[d.state] : ""}`}
                aria-current={isToday ? "date" : undefined}
                style={{
                  gridColumnStart: i === 0 ? month.offset + 1 : undefined,
                  height: "clamp(40px, 8vw, 56px)",
                  borderRadius: "10px",
                  background: "var(--card-2)",
                  boxShadow: isToday ? "inset 0 0 0 1.5px var(--accent)" : undefined,
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: "14px",
                  fontVariantNumeric: "tabular-nums",
                  color: d.state === "missed" ? "#6a6a70" : d.state === "future" ? "var(--text-2)" : "var(--text)",
                }}
              >
                {isChecked ? <Tick /> : d.day}
              </li>
            );
          })}
        </ol>

        <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", justifyContent: "space-between", gap: "16px", paddingTop: "4px" }}>
          <div style={{ display: "flex", flexDirection: "column", gap: "4px" }}>
            <b style={{ fontSize: "30px", fontWeight: 600, fontVariantNumeric: "tabular-nums" }}>
              {total} <span style={{ fontSize: "22px" }}>天</span>
            </b>
            <span class="muted small">已累计签到{run > 0 ? ` · 连续 ${run} 天` : ""}</span>
          </div>
          <button type="button" class="btn primary big" style={{ minWidth: "128px" }} disabled={buttonDisabled} onClick={() => submit()}>
            {buttonLabel}
          </button>
        </div>
      </section>

      <p class="muted small" style={{ margin: 0, lineHeight: 1.6 }}>
        签到暂不发放会员时长。记录按账户保存在这台路由器上：退出登录不会清除，重置或更换路由器后不会保留。
      </p>
    </div>
  );
}
