import type { Week } from "../api/types";

/**
 * The last `n` weeks oldest first, with the weeks nothing happened in present
 * as zeros. v_weekly_throughput only has rows for weeks with history, and a
 * chart that silently skips an empty week makes a stall look like progress.
 *
 * Weeks start on Monday, matching Postgres's date_trunc('week').
 */
export function lastWeeks(rows: Week[], n: number, now: Date = new Date()): Week[] {
  const byWeek = new Map(rows.map((r) => [r.week, r]));
  const monday = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  monday.setUTCDate(monday.getUTCDate() - ((monday.getUTCDay() + 6) % 7));
  const out: Week[] = [];
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(monday);
    d.setUTCDate(d.getUTCDate() - 7 * i);
    const key = d.toISOString().slice(0, 10);
    out.push(byWeek.get(key) ?? { week: key, opened: 0, merged: 0, closed: 0, proposed: 0, approved: 0 });
  }
  return out;
}

/** A clean axis maximum: 1, 2, 5 or 10 times a power of ten, never below `floor`. */
export function niceMax(value: number, floor = 4): number {
  const v = Math.max(value, floor);
  const pow = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 5, 10]) if (m * pow >= v) return m * pow;
  return 10 * pow;
}
