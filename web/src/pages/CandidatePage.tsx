import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { api } from "../api/client";
import { BriefView, SignalView } from "../components/BriefView";
import { Decide } from "../components/Decide";
import { Icon } from "../components/Icon";
import { PageHead } from "../components/Layout";
import { StatusPill } from "../components/Pill";
import { Failed, Skeleton } from "../components/States";
import { Time } from "../components/Time";
import { Timeline, buildEvents } from "../components/Timeline";

const REJECT_KIND: Record<string, string> = {
  human: "Your decision. Never reconsidered automatically.",
  structural: "Can never become true, so never reconsidered.",
  transient: "True today only. Comes back for another look after the cooldown.",
  quality: "Failed a configurable bar. Comes back if the bar moves.",
};

export function CandidatePage() {
  const slug = useParams().slug ?? "";
  const detail = useQuery({ queryKey: ["candidate", slug], queryFn: () => api.candidate(slug) });

  if (detail.isPending) return <Skeleton rows={6} height={56} />;
  if (detail.isError) return <Failed error={detail.error} retry={() => detail.refetch()} />;

  const { candidate: c, brief, signal, history, audit } = detail.data;
  return (
    <>
      <Link to="/candidates" className="back"><Icon name="arrow-left" size={14} /> Candidates</Link>
      <PageHead title={c.title} sub={`${c.repo} #${c.issueNumber}`}>
        <a className="btn btn-secondary" href={c.url} target="_blank" rel="noreferrer">
          Issue on GitHub <Icon name="external" size={14} />
        </a>
        {c.prId && (
          <Link className="btn btn-secondary" to={`/prs/${c.prId}`}>
            <Icon name="pr" size={14} /> Pull request
          </Link>
        )}
      </PageHead>

      <dl className="facts">
        <div><dt>Status</dt><dd><StatusPill status={c.status} /></dd></div>
        <div><dt>Contest</dt><dd>{c.contest.replace("_", " ")}</dd></div>
        <div><dt>Labels</dt><dd>{c.labels.length ? c.labels.map((l) => <span key={l} className="tag">{l}</span>) : "—"}</dd></div>
        <div><dt>Issue opened</dt><dd><Time iso={c.issueCreatedAt} /></dd></div>
        <div><dt>Last change</dt><dd><Time iso={c.updatedAt} /></dd></div>
      </dl>

      {c.status === "rejected" && c.rejectReason && (
        <p className="note">
          <Icon name="ban" size={14} />
          <span>
            <strong>{c.rejectReason.replace(/^human rejection: /, "")}</strong>
            {c.rejectKind && <> — {REJECT_KIND[c.rejectKind] ?? c.rejectKind}</>}
          </span>
        </p>
      )}
      {c.blockers.length > 0 && (
        <ul className="flags">
          {c.blockers.map((b) => <li key={b} className="flag tone-critical"><Icon name="hand" size={14} /> {b}</li>)}
        </ul>
      )}
      {c.scoreFailures.length > 0 && (
        <ul className="flags">
          {c.scoreFailures.map((b) => <li key={b} className="flag tone-warning"><Icon name="gauge" size={14} /> failed: {b.replaceAll("_", " ")}</li>)}
        </ul>
      )}

      <Decide slug={c.slug} status={c.status} />

      <div className="grid-2 grid-wide-left">
        <section className="panel" aria-labelledby="brief-title">
          <div className="panel-head"><h2 id="brief-title">Brief</h2></div>
          <BriefView brief={brief} />
          {signal && <SignalView signal={signal} />}
        </section>
        <section className="panel" aria-labelledby="hist-title">
          <div className="panel-head"><h2 id="hist-title">Lifecycle</h2></div>
          <Timeline events={buildEvents(history)} />
        </section>
      </div>

      {audit.length > 0 && (
        <section className="panel" aria-labelledby="audit-title">
          <div className="panel-head"><h2 id="audit-title">Audit entries</h2></div>
          <AuditTable rows={audit} showSlug={false} />
        </section>
      )}
    </>
  );
}

export function AuditTable({ rows, showSlug = true }: { rows: { id: number; action: string; slug: string | null; detail: string | null; actor: string | null; at: string }[]; showSlug?: boolean }) {
  return (
    <div className="table-wrap">
      <table className="table table-compact table-long">
        <thead>
          <tr>
            <th scope="col">When</th>
            <th scope="col">Action</th>
            {showSlug && <th scope="col">Candidate</th>}
            <th scope="col">Detail</th>
            <th scope="col">By</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={a.id}>
              <td><Time iso={a.at} /></td>
              <td><span className="tag">{a.action}</span></td>
              {showSlug && <td className="mono small">{a.slug ? <Link to={`/candidates/${a.slug}`}>{a.slug}</Link> : "—"}</td>}
              <td className="cell-why">{a.detail ?? "—"}</td>
              <td>{a.actor ?? <span className="muted">engine</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
