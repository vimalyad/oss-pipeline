import { useState } from "react";
import type { Week } from "../api/types";
import { shortDay } from "../lib/time";
import { useWidth } from "../lib/useWidth";
import { niceMax } from "../lib/weeks";
import { Icon } from "./Icon";

const H = 220;
const PAD = { top: 16, right: 12, bottom: 28, left: 32 };
const SERIES = [
  { key: "opened", label: "Opened", color: "var(--series-1)" },
  { key: "merged", label: "Merged", color: "var(--series-2)" },
] as const;

/**
 * Pull requests opened and merged per week, against the weekly cap.
 *
 * Grouped columns rather than lines: weeks are discrete buckets and the
 * question is "did this week make ten", which a bar against a reference line
 * answers directly. The table view carries every value for anyone who cannot
 * use the hover layer.
 */
export function ThroughputChart({ weeks, cap }: { weeks: Week[]; cap: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const [asTable, setAsTable] = useState(false);

  const max = niceMax(Math.max(cap, ...weeks.flatMap((w) => [w.opened, w.merged])));
  const plotW = Math.max(width - PAD.left - PAD.right, 100);
  const plotH = H - PAD.top - PAD.bottom;
  const band = plotW / weeks.length;
  const bar = Math.min(18, Math.max(4, (band - 10) / 2));
  const y = (v: number) => PAD.top + plotH - (v / max) * plotH;
  const ticks = [0, max / 2, max];
  const active = hover === null ? undefined : weeks[hover];
  // Label every nth week counting back from the current one, so the latest
  // week is always labelled and no two labels can collide.
  const labelEvery = Math.max(1, Math.ceil(56 / band));

  return (
    <figure className="chart">
      <figcaption className="chart-head">
        <div>
          <h3 className="chart-title">Weekly throughput</h3>
          <p className="chart-sub">Last {weeks.length} weeks, against the cap of {cap} a week</p>
        </div>
        <div className="chart-tools">
          <ul className="legend" aria-label="Legend">
            {SERIES.map((s) => (
              <li key={s.key}>
                <span className="swatch" style={{ background: s.color }} />
                {s.label}
              </li>
            ))}
          </ul>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            aria-pressed={asTable}
            onClick={() => setAsTable((t) => !t)}
          >
            <Icon name={asTable ? "chart" : "table"} size={14} />
            {asTable ? "Chart" : "Table"}
          </button>
        </div>
      </figcaption>

      {asTable ? (
        <div className="table-wrap">
          <table className="table table-compact">
            <thead>
              <tr><th scope="col">Week of</th><th scope="col" className="num">Opened</th><th scope="col" className="num">Merged</th><th scope="col" className="num">Closed</th></tr>
            </thead>
            <tbody>
              {[...weeks].reverse().map((w) => (
                <tr key={w.week}>
                  <td>{shortDay(w.week)}</td>
                  <td className="num">{w.opened}</td>
                  <td className="num">{w.merged}</td>
                  <td className="num">{w.closed}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div ref={ref} className="chart-plot" onMouseLeave={() => setHover(null)}>
          <svg width={width} height={H} role="img" aria-label={`Opened and merged pull requests per week for the last ${weeks.length} weeks`}>
            {ticks.map((t) => (
              <g key={t}>
                <line x1={PAD.left} x2={PAD.left + plotW} y1={y(t)} y2={y(t)} className={t === 0 ? "axis" : "grid"} />
                <text x={PAD.left - 8} y={y(t)} dy="0.32em" textAnchor="end" className="tick">{t}</text>
              </g>
            ))}
            <line x1={PAD.left} x2={PAD.left + plotW} y1={y(cap)} y2={y(cap)} className="ref" />
            <text x={PAD.left + plotW} y={y(cap) - 6} textAnchor="end" className="ref-label">cap {cap}</text>

            {weeks.map((w, i) => {
              const x0 = PAD.left + i * band + (band - (bar * 2 + 2)) / 2;
              return (
                <g
                  key={w.week}
                  tabIndex={0}
                  aria-label={`Week of ${shortDay(w.week)}: ${w.opened} opened, ${w.merged} merged`}
                  onMouseEnter={() => setHover(i)}
                  onFocus={() => setHover(i)}
                  onBlur={() => setHover(null)}
                  className="band"
                >
                  <rect x={PAD.left + i * band} y={PAD.top} width={band} height={plotH} className={hover === i ? "band-hit on" : "band-hit"} />
                  {SERIES.map((s, j) => {
                    const v = w[s.key];
                    if (v === 0) return null;
                    const top = y(v);
                    const h = PAD.top + plotH - top;
                    const x = x0 + j * (bar + 2);
                    const r = Math.min(4, bar / 2, h);
                    // Rounded data-end, square at the baseline.
                    const d = `M${x},${top + h} V${top + r} Q${x},${top} ${x + r},${top} H${x + bar - r} Q${x + bar},${top} ${x + bar},${top + r} V${top + h} Z`;
                    return <path key={s.key} d={d} fill={s.color} />;
                  })}
                  {(weeks.length - 1 - i) % labelEvery === 0 && (
                    <text x={PAD.left + i * band + band / 2} y={H - 8} textAnchor="middle" className="tick">
                      {shortDay(w.week)}
                    </text>
                  )}
                </g>
              );
            })}
          </svg>
          {active && hover !== null && (
            <div
              className="tooltip"
              role="status"
              style={{
                left: Math.min(Math.max(PAD.left + hover * band + band / 2, 80), width - 80),
                top: PAD.top,
              }}
            >
              <p className="tooltip-title">Week of {shortDay(active.week)}</p>
              {SERIES.map((s) => (
                <p key={s.key} className="tooltip-row">
                  <span className="swatch" style={{ background: s.color }} />
                  {s.label}
                  <strong>{active[s.key]}</strong>
                </p>
              ))}
            </div>
          )}
        </div>
      )}
    </figure>
  );
}
