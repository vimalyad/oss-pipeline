import type { Brief, Signal } from "../api/types";
import { Icon } from "./Icon";

/**
 * The specification a patch is written against. Shown in full because it is
 * the thing being approved: the approach to build, what the maintainers have
 * already ruled out, and the definition of done.
 */
export function BriefView({ brief }: { brief: Brief | null }) {
  if (!brief) {
    return <p className="muted">No brief extracted yet.</p>;
  }
  return (
    <dl className="brief">
      <div>
        <dt>Maintainer's approach</dt>
        <dd>
          {brief.maintainerApproach ? (
            <>
              {brief.maintainerApproach}
              {(brief.approachAssociation || brief.approachSourceUrl) && (
                <span className="brief-src">
                  {brief.approachAssociation && <span className="tag">{brief.approachAssociation.toLowerCase()}</span>}
                  {brief.approachSourceUrl && (
                    <a className="link-quiet" href={brief.approachSourceUrl} target="_blank" rel="noreferrer">
                      source <Icon name="external" size={12} />
                    </a>
                  )}
                </span>
              )}
            </>
          ) : (
            <span className="muted">None stated — the acceptance criteria are the spec</span>
          )}
        </dd>
      </div>
      {brief.acceptanceCriteria.length > 0 && (
        <div>
          <dt>Done when</dt>
          <dd>
            <ul className="checklist">
              {brief.acceptanceCriteria.map((a) => (
                <li key={a}><Icon name="check" size={14} /> {a}</li>
              ))}
            </ul>
          </dd>
        </div>
      )}
      {brief.rejectedApproaches.length > 0 && (
        <div>
          <dt>Ruled out</dt>
          <dd>
            <ul className="checklist checklist-no">
              {brief.rejectedApproaches.map((a) => (
                <li key={a}><Icon name="ban" size={14} /> {a}</li>
              ))}
            </ul>
          </dd>
        </div>
      )}
      {brief.openQuestions.length > 0 && (
        <div>
          <dt>Open questions</dt>
          <dd><ul className="plain">{brief.openQuestions.map((q) => <li key={q}>{q}</li>)}</ul></dd>
        </div>
      )}
      {brief.reproduction && (
        <div>
          <dt>Reproduction</dt>
          <dd><pre className="code">{brief.reproduction}</pre></dd>
        </div>
      )}
      {brief.dropped.length > 0 && (
        <div>
          <dt>Dropped by the checks</dt>
          <dd><ul className="plain muted">{brief.dropped.map((d) => <li key={d}>{d}</li>)}</ul></dd>
        </div>
      )}
    </dl>
  );
}

export function SignalView({ signal }: { signal: Signal }) {
  return (
    <div className="signal">
      <p className="signal-head">
        <Icon name="pr" size={14} />
        Existing pull request{" "}
        {signal.url ? (
          <a href={signal.url} target="_blank" rel="noreferrer">#{signal.prNumber}</a>
        ) : (
          `#${signal.prNumber}`
        )}
        {signal.author && <> by {signal.author}</>}
        {signal.draft && <span className="tag">draft</span>}
        <span className="tag">{signal.reviewed ? "reviewed" : "never reviewed"}</span>
      </p>
      {signal.reasons.length > 0 && (
        <ul className="plain muted">{signal.reasons.map((r) => <li key={r}>{r}</li>)}</ul>
      )}
    </div>
  );
}
