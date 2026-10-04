import { useQueries, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { api } from "../api/client";
import type { CandidateDetail, CandidateSummary, Decision } from "../api/types";
import { BriefView, SignalView } from "../components/BriefView";
import { Decide } from "../components/Decide";
import { Icon } from "../components/Icon";
import { PageHead } from "../components/Layout";
import { Empty, Failed, Skeleton } from "../components/States";
import { Time } from "../components/Time";

function Proposal({ c, detail, onDecided }: { c: CandidateSummary; detail: CandidateDetail | undefined; onDecided: (d: Decision) => void }) {
  return (
    <article className="proposal" aria-labelledby={`p-${c.id}`}>
      <header className="proposal-head">
        <p className="eyebrow mono">
          {c.repo} <a href={c.url} target="_blank" rel="noreferrer" className="link-quiet">#{c.issueNumber} <Icon name="external" size={12} /></a>
        </p>
        <h2 id={`p-${c.id}`} className="proposal-title">
          <Link to={`/candidates/${c.slug}`}>{c.title}</Link>
        </h2>
        <p className="proposal-meta">
          {c.labels.map((l) => <span key={l} className="tag">{l}</span>)}
          <span className="muted">proposed <Time iso={c.updatedAt} /></span>
        </p>
      </header>

      {(c.softPenalties.length > 0 || c.blockers.length > 0) && (
        <ul className="flags">
          {c.blockers.map((b) => (
            <li key={b} className="flag tone-critical"><Icon name="hand" size={14} /> {b}</li>
          ))}
          {c.softPenalties.map((p) => (
            <li key={p} className="flag tone-warning"><Icon name="alert" size={14} /> {p}</li>
          ))}
        </ul>
      )}

      {detail ? (
        <>
          <BriefView brief={detail.brief} />
          {detail.signal && <SignalView signal={detail.signal} />}
        </>
      ) : (
        <Skeleton rows={2} height={20} />
      )}

      <footer className="proposal-foot">
        <Decide slug={c.slug} status={c.status} onDecided={onDecided} />
      </footer>
    </article>
  );
}

interface Decided extends Decision {
  title: string;
}

/** What happened to the cards that just left the list, newest first. */
function DecidedBanner({ items, dismiss }: { items: Decided[]; dismiss: () => void }) {
  return (
    <div className="decided" role="status" aria-live="polite">
      {items.length > 0 && (
        <>
          <ul>
            {items.map((d) => (
              <li key={d.slug} className={d.status === "approved" ? "tone-good" : "tone-muted"}>
                <Icon name={d.status === "approved" ? "check-circle" : "ban"} size={14} />
                <span>
                  <strong>{d.status === "approved" ? "Approved" : "Rejected"}</strong>{" "}
                  <Link to={`/candidates/${d.slug}`}>{d.title}</Link>
                  {d.status === "approved" && <span className="muted"> — queued for the next implement run</span>}
                </span>
              </li>
            ))}
          </ul>
          <button type="button" className="btn btn-ghost btn-sm" onClick={dismiss}>
            Dismiss
          </button>
        </>
      )}
    </div>
  );
}

export function ProposalsPage() {
  const [decided, setDecided] = useState<Decided[]>([]);
  const list = useQuery({ queryKey: ["candidates", ["proposed"]], queryFn: () => api.candidates(["proposed"]) });
  // Each card's brief, fetched in parallel once the list is known.
  const details = useQueries({
    queries: (list.data ?? []).map((c) => ({
      queryKey: ["candidate", c.slug],
      queryFn: () => api.candidate(c.slug),
    })),
  });

  return (
    <>
      <PageHead
        title="Proposals"
        sub="Issues that cleared every bar. Nothing is implemented, and no pull request opens, until you approve it."
      />
      <DecidedBanner items={decided} dismiss={() => setDecided([])} />
      {list.isPending ? (
        <Skeleton rows={3} height={180} />
      ) : list.isError ? (
        <Failed error={list.error} retry={() => list.refetch()} />
      ) : list.data.length === 0 ? (
        <Empty icon="inbox" title="No proposals waiting">The next discovery run will add any new ones.</Empty>
      ) : (
        <div className="proposals">
          {list.data.map((c, i) => (
            <Proposal
              key={c.slug}
              c={c}
              detail={details[i]?.data}
              onDecided={(d) => d.changed && setDecided((prev) => [{ ...d, title: c.title }, ...prev.filter((p) => p.slug !== d.slug)])}
            />
          ))}
        </div>
      )}
    </>
  );
}
