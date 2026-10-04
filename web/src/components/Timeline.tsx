import type { CandidateStatus, Feedback, HistoryEntry, PrState, StateChange } from "../api/types";
import { CANDIDATE_STATUS, PR_STATE } from "../lib/status";
import { Icon, type IconName } from "./Icon";
import { Time } from "./Time";

/**
 * One timeline from three clocks: the candidate's lifecycle (what the
 * pipeline decided), the pull request's observed state (what GitHub shows) and
 * maintainer feedback (what people said). Interleaved by time, because "CI
 * went red an hour after the reviewer asked for changes" is only visible when
 * they share an axis.
 */
type Event =
  | { kind: "lifecycle"; at: string; entry: HistoryEntry }
  | { kind: "pr"; at: string; entry: StateChange }
  | { kind: "feedback"; at: string; entry: Feedback };

export function buildEvents(
  lifecycle: HistoryEntry[] = [],
  states: StateChange[] = [],
  feedback: Feedback[] = [],
): Event[] {
  const events: Event[] = [
    ...lifecycle.map((entry) => ({ kind: "lifecycle" as const, at: entry.at, entry })),
    ...states.map((entry) => ({ kind: "pr" as const, at: entry.at, entry })),
    ...feedback.map((entry) => ({ kind: "feedback" as const, at: entry.createdAt, entry })),
  ];
  // Newest first: the question is nearly always "what happened last".
  return events.sort((a, b) => b.at.localeCompare(a.at));
}

function Marker({ icon, tone }: { icon: IconName; tone: string }) {
  return (
    <span className={`tl-marker tone-${tone}`}>
      <Icon name={icon} size={14} />
    </span>
  );
}

function Lifecycle({ e }: { e: HistoryEntry }) {
  const meta = CANDIDATE_STATUS[e.to];
  return (
    <>
      <Marker icon={meta.icon} tone={meta.tone} />
      <div className="tl-body">
        <p className="tl-line">
          <span className="tl-source">Pipeline</span>
          {e.from ? (
            <>
              {label(e.from)} <span aria-hidden="true">→</span><span className="sr-only">to</span> <strong>{meta.label}</strong>
            </>
          ) : (
            <strong>{meta.label}</strong>
          )}
          {e.forced && <span className="tag tag-warn">forced</span>}
        </p>
        {(e.note || e.actor) && (
          <p className="tl-note">
            {e.note}
            {e.actor && <span className="tl-actor"> — {e.actor}</span>}
          </p>
        )}
      </div>
    </>
  );
}

const label = (s: CandidateStatus) => CANDIDATE_STATUS[s].label;
const prLabel = (s: PrState) => PR_STATE[s].label;

function PrChange({ e }: { e: StateChange }) {
  const meta = PR_STATE[e.to];
  return (
    <>
      <Marker icon={meta.icon} tone={meta.tone} />
      <div className="tl-body">
        <p className="tl-line">
          <span className="tl-source">GitHub</span>
          {e.from ? (
            <>
              {prLabel(e.from)} <span aria-hidden="true">→</span><span className="sr-only">to</span> <strong>{meta.label}</strong>
            </>
          ) : (
            <strong>{meta.label}</strong>
          )}
        </p>
        {e.detail && <p className="tl-note">{e.detail}</p>}
      </div>
    </>
  );
}

function FeedbackItem({ e }: { e: Feedback }) {
  return (
    <>
      <Marker icon="message" tone={e.posted ? "neutral" : "warning"} />
      <div className="tl-body">
        <p className="tl-line">
          <span className="tl-source">{e.author}</span>
          {e.authorAssociation && <span className="tag">{e.authorAssociation.toLowerCase()}</span>}
          {e.classification && <span className="tag">{e.classification.replace("_", " ")}</span>}
          {!e.posted && <span className="tag tag-warn">awaiting your reply</span>}
        </p>
        <blockquote className="tl-quote">{e.body}</blockquote>
        {e.draft && (
          <div className={e.posted ? "tl-reply" : "tl-reply is-draft"}>
            <p className="tl-reply-head">
              <Icon name="reply" size={14} />
              {e.posted ? "Your reply" : "Drafted reply, not sent"}
              {e.postedAt && <Time iso={e.postedAt} className="muted" />}
            </p>
            <p>{e.draft}</p>
            {!e.posted && (
              <p className="tl-hint">
                Send it from the CLI with <code>pipeline replies post</code>. The dashboard can't post to GitHub.
              </p>
            )}
          </div>
        )}
        {e.url && (
          <a className="link-quiet" href={e.url} target="_blank" rel="noreferrer">
            View on GitHub <Icon name="external" size={12} />
          </a>
        )}
      </div>
    </>
  );
}

export function Timeline({ events }: { events: Event[] }) {
  if (events.length === 0) return <p className="muted">Nothing recorded yet.</p>;
  return (
    <ol className="timeline">
      {events.map((ev, i) => (
        <li key={`${ev.kind}-${ev.at}-${i}`} className={`tl-item tl-${ev.kind}`}>
          {ev.kind === "lifecycle" && <Lifecycle e={ev.entry} />}
          {ev.kind === "pr" && <PrChange e={ev.entry} />}
          {ev.kind === "feedback" && <FeedbackItem e={ev.entry} />}
          <Time iso={ev.at} className="tl-time" />
        </li>
      ))}
    </ol>
  );
}
