/** Calendar maths for the daily check-in page (pure, no imports, unit-tested in tests/). */

export type DayState = "checked" | "today" | "today-checked" | "missed" | "future";

export interface DayCell {
  key: string;
  day: number;
  state: DayState;
}

export interface MonthView {
  year: number;
  /** 0-11 */
  month: number;
  /** blank cells before day 1 in a Monday-first week */
  offset: number;
  days: DayCell[];
  /** check-ins in this month */
  count: number;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** Local calendar date as YYYY-MM-DD */
export function dateKey(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function monthKey(year: number, month: number): string {
  return `${year}-${pad(month + 1)}`;
}

/** The day before a YYYY-MM-DD key (noon avoids DST edges) */
export function previousDay(key: string): string {
  const [y, m, d] = key.split("-").map(Number);
  return dateKey(new Date(y, m - 1, d - 1, 12));
}

export function monthView(year: number, month: number, checked: ReadonlySet<string>, today: string): MonthView {
  const first = new Date(year, month, 1, 12);
  const total = new Date(year, month + 1, 0, 12).getDate();
  const days: DayCell[] = [];
  let count = 0;
  for (let day = 1; day <= total; day++) {
    const key = `${monthKey(year, month)}-${pad(day)}`;
    const done = checked.has(key);
    if (done) count++;
    const state: DayState = key === today ? (done ? "today-checked" : "today") : done ? "checked" : key < today ? "missed" : "future";
    days.push({ key, day, state });
  }
  return { year, month, offset: (first.getDay() + 6) % 7, days, count };
}

/** Days in a row up to today, or up to yesterday while today is still open */
export function streak(checked: ReadonlySet<string>, today: string): number {
  let key = checked.has(today) ? today : previousDay(today);
  let n = 0;
  while (checked.has(key)) {
    n++;
    key = previousDay(key);
  }
  return n;
}

/** Months the calendar can show: from the first check-in (or this month) to this month */
export function monthRange(dates: readonly string[], today: string): { first: [number, number]; last: [number, number] } {
  const [ty, tm] = today.split("-").map(Number);
  const earliest = dates.length ? dates.reduce((a, b) => (a < b ? a : b)) : today;
  const [fy, fm] = (earliest < today ? earliest : today).split("-").map(Number);
  return { first: [fy, fm - 1], last: [ty, tm - 1] };
}
