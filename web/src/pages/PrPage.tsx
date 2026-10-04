import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { api } from "../api/client";
import { Icon } from "../components/Icon";
import { PageHead } from "../components/Layout";
import { PrStatePill, StatusPill } from "../components/Pill";
import { Failed, Skeleton } from "../components/States";
import { Time } from "../components/Time";
import { Timeline, buildEvents } from "../components/Timeline";
import { Checks } from "./PrsPage";

export function PrPage() {
  const id = Number(useParams().id);
  const detail = useQuery({ queryKey: ["pr", id], queryFn: () => api.pr(id), enabled: Number.isFinite(id) });

  if (detail.isPending) return <Skeleton rows={6} height={56} />;
  if (detail.isError) return <Failed error={detail.error} retry={() => detail.refetch()} />;

  const { pr, candidate, states, lifecycle, feedback } = detail.data;
  const unanswered = feedback.filter((f) => !f.posted).length;

  return (
    <>
      <Link to="/prs" className="back"><Icon name="arrow-left" size={14} /> Pull requests</Link>
      <PageHead title={pr.title} sub={`${pr.repo} #${pr.number}`}>
        <a className="btn btn-secondary" href={pr.prUrl} target="_blank" rel="noreferrer">
          Open on GitHub <Icon name="external" size={14} />
        </a>
      </PageHead>

      <dl className="facts">
        <div><dt>State</dt><dd><PrStatePill state={pr.state} /></dd></div>
        <div><dt>Checks</dt><dd><Checks pr={pr} /></dd></div>
        <div><dt>Review</dt><dd>{pr.reviewDecision?.replace("_", " ").toLowerCase() ?? "none yet"}{pr.reviewerCount > 0 && ` · ${pr.reviewerCount} reviewer${pr.reviewerCount > 1 ? "s" : ""}`}</dd></div>
        <div><dt>Diff</dt><dd>{pr.additions === null ? "—" : <><span className="mono add">+{pr.additions}</span> <span className="mono del">−{pr.deletions}</span> in {pr.changedFiles} file{pr.changedFiles === 1 ? "" : "s"}</>}</dd></div>
        <div><dt>Opened</dt><dd><Time iso={pr.openedAt} /></dd></div>
        <div><dt>{pr.mergedAt ? "Merged" : pr.closedAt ? "Closed" : "Idle for"}</dt>
          <dd>{pr.mergedAt ? <Time iso={pr.mergedAt} /> : pr.closedAt ? <Time iso={pr.closedAt} /> : `${pr.idleDays ?? 0} day${pr.idleDays === 1 ? "" : "s"}`}</dd></div>
        <div><dt>Issue</dt><dd><a href={pr.issueUrl} target="_blank" rel="noreferrer">#{pr.issueNumber} <Icon name="external" size={12} /></a></dd></div>
        {candidate && (
          <div><dt>Candidate</dt><dd><Link to={`/candidates/${candidate.slug}`}><StatusPill status={candidate.status} /></Link></dd></div>
        )}
      </dl>

      {pr.tookOver && (
        <p className="note"><Icon name="refresh" size={14} /> Continues someone else's abandoned work{pr.credits ? ` — credited to ${pr.credits}` : ""}.</p>
      )}

      <section className="panel" aria-labelledby="tl-title">
        <div className="panel-head">
          <h2 id="tl-title">History</h2>
          <p className="panel-sub">
            Pipeline decisions, GitHub state and maintainer feedback on one timeline, newest first.
            {unanswered > 0 && <strong> {unanswered} comment{unanswered > 1 ? "s" : ""} waiting on you.</strong>}
          </p>
        </div>
        <Timeline events={buildEvents(lifecycle, states, feedback)} />
        {states.length === 0 && (
          <p className="muted small">No GitHub state recorded yet; it appears after the next watch cycle.</p>
        )}
      </section>
    </>
  );
}
