import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { api } from "../api/client";
import type { FunnelStage, NeedsYouItem } from "../api/types";
import { FunnelChart } from "../components/FunnelChart";
import { Icon, type IconName } from "../components/Icon";
import { PageHead } from "../components/Layout";
import { Meter } from "../components/Meter";
import { Empty, Failed, Skeleton } from "../components/States";
import { ThroughputChart } from "../components/ThroughputChart";
import { Time } from "../components/Time";
import { lastWeeks } from "../lib/weeks";

const NEED: Record<NeedsYouItem["what"], { icon: IconName; label: string; tone: string }> = {
  reply: { icon: "message", label: "Reply waiting", tone: "warning" },
  blocker: { icon: "hand", label: "Blocked", tone: "critical" },
  proposal: { icon: "inbox", label: "Proposal", tone: "info" },
};

function needLink(n: NeedsYouItem) {
  if (n.what === "reply" && n.prId) return `/prs/${n.prId}`;
  return n.slug ? `/candidates/${n.slug}` : null;
}

/**
 * Approved and auto-approved are one stage to a reader, and two rows with the
 * second usually zero would make the conversion after them meaningless.
 */
function mergeApprovals(stages: FunnelStage[]): FunnelStage[] {
  const auto = stages.find((s) => s.stage === "auto_approved")?.reached ?? 0;
  return stages
    .filter((s) => s.stage !== "auto_approved")
    .map((s) => (s.stage === "approved" ? { ...s, reached: s.reached + auto } : s));
}

export function OverviewPage() {
  // Independent queries, issued together: no request waits on another.
  const overview = useQuery({ queryKey: ["overview"], queryFn: api.overview });
  const needs = useQuery({ queryKey: ["needs-you"], queryFn: api.needsYou });
  const weeks = useQuery({ queryKey: ["throughput", 12], queryFn: () => api.throughput(12) });
  const funnel = useQuery({ queryKey: ["funnel"], queryFn: api.funnel });

  const o = overview.data;
  return (
    <>
      <PageHead title="Overview" sub="What the engine is allowed to do, how much of it is spent, and what is waiting on you." />

      <section aria-label="Caps" className="meters">
        {overview.isPending ? (
          <Skeleton rows={1} height={120} />
        ) : overview.isError ? (
          <Failed error={overview.error} retry={() => overview.refetch()} />
        ) : (
          o && (
            <>
              <Meter label="Opened, last 7 days" used={o.usage.openedThisWeek} cap={o.caps.prsPerWeek} hint="The weekly target" />
              <Meter label="Opened today" used={o.usage.openedToday} cap={o.caps.prsPerDay} hint="Daily ceiling, with slack for uneven weeks" />
              <Meter label="Open right now" used={o.usage.open} cap={o.caps.maxOpenPrs} hint={`At most ${o.caps.maxOpenPerRepo} on any one project`} />
              <div className="meter meter-plain">
                <p className="meter-label">Merged, all time</p>
                <p className="meter-value"><span className="meter-used">{o.mergedAllTime}</span></p>
                <p className="meter-hint">
                  <Link to="/prs?group=done">See finished work</Link>
                </p>
              </div>
            </>
          )
        )}
      </section>

      <section className="panel" aria-labelledby="needs-title">
        <div className="panel-head">
          <h2 id="needs-title">Needs you</h2>
          <p className="panel-sub">Replies first, then blockers, then proposals: a maintainer waiting outranks a proposal nobody is waiting for.</p>
        </div>
        {needs.isPending ? (
          <Skeleton rows={3} />
        ) : needs.isError ? (
          <Failed error={needs.error} retry={() => needs.refetch()} />
        ) : needs.data.length === 0 ? (
          <Empty title="Nothing is waiting on you">The engine will notify your phone when something is.</Empty>
        ) : (
          <ul className="needs">
            {needs.data.map((n) => {
              const meta = NEED[n.what];
              const to = needLink(n);
              return (
                <li key={`${n.what}-${n.refId}`} className="need">
                  <span className={`need-kind tone-${meta.tone}`}>
                    <Icon name={meta.icon} size={14} />
                    {meta.label}
                  </span>
                  <div className="need-main">
                    {to ? <Link to={to} className="need-title">{n.title}</Link> : <span className="need-title">{n.title}</span>}
                    <p className="need-meta">
                      <span className="mono">{n.repo}#{n.issueNumber}</span>
                      {n.author && <> · from {n.author}</>}
                    </p>
                  </div>
                  <Time iso={n.since} className="need-time" />
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <div className="grid-2">
        <section className="panel">
          {weeks.isPending ? (
            <Skeleton rows={1} height={260} />
          ) : weeks.isError ? (
            <Failed error={weeks.error} retry={() => weeks.refetch()} />
          ) : (
            <ThroughputChart weeks={lastWeeks(weeks.data, 12)} cap={o?.caps.prsPerWeek ?? 10} />
          )}
        </section>
        <section className="panel">
          {funnel.isPending ? (
            <Skeleton rows={6} height={24} />
          ) : funnel.isError ? (
            <Failed error={funnel.error} retry={() => funnel.refetch()} />
          ) : funnel.data.length === 0 ? (
            <Empty icon="chart" title="No history yet">The funnel fills in once discovery has run.</Empty>
          ) : (
            <FunnelChart stages={mergeApprovals(funnel.data)} />
          )}
        </section>
      </div>
    </>
  );
}
