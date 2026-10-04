import type { CandidateStatus, PrState } from "../api/types";
import { CANDIDATE_STATUS, PR_STATE, type StateMeta } from "../lib/status";
import { Icon } from "./Icon";

function Pill({ meta }: { meta: StateMeta }) {
  return (
    <span className={`pill tone-${meta.tone}`}>
      <Icon name={meta.icon} size={14} />
      {meta.label}
    </span>
  );
}

export const PrStatePill = ({ state }: { state: PrState }) => <Pill meta={PR_STATE[state]} />;

export const StatusPill = ({ status }: { status: CandidateStatus }) => (
  <Pill meta={CANDIDATE_STATUS[status]} />
);
