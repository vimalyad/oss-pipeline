// A small inline SVG set: no icon font, no emoji. Every glyph is drawn on a
// 16px grid with a 1.5px stroke so they sit evenly beside 14px text.

const PATHS = {
  check: "M3.5 8.5l3 3 6-7",
  "check-circle": "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM5.5 8.2l1.8 1.8 3.4-3.8",
  "x-circle": "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM5.8 5.8l4.4 4.4M10.2 5.8l-4.4 4.4",
  x: "M4 4l8 8M12 4l-8 8",
  clock: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM8 4.5V8l2.5 1.5",
  eye: "M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8zM8 10a2 2 0 1 0 0-4 2 2 0 0 0 0 4z",
  message: "M2.5 3.5h11v7h-6l-3 2.5v-2.5h-2z",
  split: "M4 2.5v11M12 2.5v3a3 3 0 0 1-3 3H4M12 13.5v-2",
  hand: "M5 8V3.5a1 1 0 0 1 2 0V7m0-4.5a1 1 0 0 1 2 0V7m0-3.5a1 1 0 0 1 2 0V8.5m0-2a1 1 0 0 1 2 0V10a4.5 4.5 0 0 1-4.5 4.5h-.6a4 4 0 0 1-3.1-1.5L3 10.5a1 1 0 0 1 1.5-1.3L5 9.8",
  refresh: "M13.5 3.5v3h-3M2.5 12.5v-3h3M3.3 6.5a5 5 0 0 1 9.4-.5M12.7 9.5a5 5 0 0 1-9.4.5",
  pr: "M4.5 5.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM4.5 14.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM11.5 14.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM4.5 5.5v5M11.5 10.5V6a2 2 0 0 0-2-2H7.5",
  "pr-closed": "M4.5 5.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM4.5 14.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM11.5 14.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM4.5 5.5v5M11.5 10.5V8M10 2.5l3 3M13 2.5l-3 3",
  merge: "M4.5 5.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM4.5 14.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM11.5 10.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM4.5 5.5v5M4.5 5.5a4 4 0 0 0 5 3",
  pause: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM6.5 5.5v5M9.5 5.5v5",
  search: "M7 12a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM10.5 10.5l3.5 3.5",
  gauge: "M2.5 11a5.5 5.5 0 1 1 11 0M8 11l2.5-3.5",
  inbox: "M2 9.5l1.8-6h8.4l1.8 6v4H2zM2 9.5h3.5l1 1.5h3l1-1.5H14",
  bolt: "M9 1.5L3.5 9H8l-1 5.5L12.5 7H8z",
  ban: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM3.4 3.4l9.2 9.2",
  code: "M5.5 4.5L2 8l3.5 3.5M10.5 4.5L14 8l-3.5 3.5",
  alert: "M8 2l6.5 11.5h-13zM8 6.5v3M8 11.5v.01",
  upload: "M8 10.5V2.5M5 5.5l3-3 3 3M2.5 10.5v3h11v-3",
  home: "M2.5 7.5L8 3l5.5 4.5v6h-4v-4h-3v4h-4z",
  list: "M5.5 4h8M5.5 8h8M5.5 12h8M2.5 4h.01M2.5 8h.01M2.5 12h.01",
  log: "M3.5 1.5h6l3 3v10h-9zM9.5 1.5v3h3M5.5 8h5M5.5 11h5",
  external: "M9.5 2.5h4v4M13.5 2.5L7.5 8.5M11.5 9.5v4h-9v-9h4",
  sun: "M8 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM8 1v1.5M8 13.5V15M1 8h1.5M13.5 8H15M3 3l1 1M12 12l1 1M3 13l1-1M12 4l1-1",
  moon: "M13.5 9.5A6 6 0 0 1 6.5 2.5a6 6 0 1 0 7 7z",
  "arrow-left": "M13 8H3M7 4L3 8l4 4",
  circle: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13z",
  reply: "M6.5 4L2.5 8l4 4M2.5 8h7a4 4 0 0 1 4 4v1",
  table: "M2 3h12v10H2zM2 6.5h12M2 10h12M6 3v10",
  chart: "M2.5 2.5v11h11M5.5 11V8M8.5 11V5M11.5 11V7",
} as const;

export type IconName = keyof typeof PATHS;

interface IconProps {
  name: IconName;
  size?: number;
  /** Set when the icon is the only thing conveying meaning (an icon-only button). */
  label?: string;
  className?: string;
}

export function Icon({ name, size = 16, label, className }: IconProps) {
  return (
    <svg
      className={className ? `icon ${className}` : "icon"}
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      focusable="false"
    >
      <path d={PATHS[name]} />
    </svg>
  );
}
