import type { FunnelStage } from "../api/types";
import type { CandidateStatus } from "../api/types";
import { CANDIDATE_STATUS } from "../lib/status";

// A stage something passed through, not where it is now: "Awaiting you" is
// right for a status and wrong for a count of everything ever proposed.
const STAGE_LABEL: Partial<Record<CandidateStatus, string>> = { proposed: "Proposed", pr_open: "PR opened" };
const stageLabel = (s: CandidateStatus) => STAGE_LABEL[s] ?? CANDIDATE_STATUS[s].label;

/**
 * How many candidates ever reached each stage. One series, so no legend: the
 * title says what is plotted. Values sit at the bar tips, which is all the
 * labelling a single ordered series needs; the conversion from the stage
 * before is the number that explains the throughput.
 */
export function FunnelChart({ stages }: { stages: FunnelStage[] }) {
  const max = Math.max(1, ...stages.map((s) => s.reached));
  return (
    <figure className="chart">
      <figcaption className="chart-head">
        <div>
          <h3 className="chart-title">Funnel</h3>
          <p className="chart-sub">Candidates that ever reached each stage, all time</p>
        </div>
      </figcaption>
      <ol className="funnel">
        {stages.map((s, i) => {
          const prev = i > 0 ? stages[i - 1] : undefined;
          const rate = prev && prev.reached > 0 ? Math.round((s.reached / prev.reached) * 100) : null;
          return (
            <li key={s.stage} className="funnel-row">
              <span className="funnel-label">{stageLabel(s.stage)}</span>
              <span className="funnel-track">
                <span className="funnel-bar" style={{ width: `${(s.reached / max) * 100}%` }} />
                <span className="funnel-value">{s.reached}</span>
              </span>
              <span className="funnel-rate" title={prev ? `of ${prev.reached} at ${stageLabel(prev.stage)}` : undefined}>
                {rate === null ? "" : `${rate}%`}
              </span>
            </li>
          );
        })}
      </ol>
    </figure>
  );
}
