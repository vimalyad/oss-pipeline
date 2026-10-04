const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto", style: "short" });
const abs = new Intl.DateTimeFormat("en", { dateStyle: "medium", timeStyle: "short" });
const day = new Intl.DateTimeFormat("en", { month: "short", day: "numeric" });

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
];

/** "3 hr. ago", "yesterday". Coarse on purpose: a dashboard is read at a glance. */
export function relative(iso: string, now: Date = new Date()): string {
  const seconds = (new Date(iso).getTime() - now.getTime()) / 1000;
  for (const [unit, size] of UNITS) {
    if (Math.abs(seconds) >= size) return rtf.format(Math.round(seconds / size), unit);
  }
  return "just now";
}

export const absolute = (iso: string) => abs.format(new Date(iso));
export const shortDay = (iso: string) => day.format(new Date(iso));
