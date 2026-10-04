import { Icon } from "./Icon";

/**
 * One cap and how much of it is spent. At the cap the meter says so in words
 * as well as colour, because "the engine will open nothing more today" is the
 * fact that matters and it must not depend on telling amber from blue.
 */
export function Meter({ label, used, cap, hint }: { label: string; used: number; cap: number; hint: string }) {
  const full = used >= cap;
  const pct = cap > 0 ? Math.min(100, (used / cap) * 100) : 0;
  return (
    <div className={full ? "meter is-full" : "meter"}>
      <p className="meter-label">{label}</p>
      <p className="meter-value">
        <span className="meter-used">{used}</span>
        <span className="meter-cap">/ {cap}</span>
      </p>
      <div
        className="meter-track"
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={cap}
        aria-valuenow={used}
      >
        <span className="meter-fill" style={{ width: `${pct}%` }} />
      </div>
      <p className="meter-hint">
        {full ? (
          <>
            <Icon name="pause" size={14} /> At cap — nothing more opens
          </>
        ) : (
          hint
        )}
      </p>
    </div>
  );
}
