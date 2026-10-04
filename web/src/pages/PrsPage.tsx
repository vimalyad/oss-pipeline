import { useQuery } from "@tanstack/react-query";
import { useDeferredValue, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { api } from "../api/client";
import type { PrCard } from "../api/types";
import { Icon } from "../components/Icon";
import { PageHead } from "../components/Layout";
import { PrStatePill } from "../components/Pill";
import { Empty, Failed, Skeleton } from "../components/States";
import { Time } from "../components/Time";
import { PR_GROUPS, PR_STATE, type PrGroup } from "../lib/status";

export function Checks({ pr }: { pr: PrCard }) {
  if (pr.checksTotal === 0) return <span className="muted">—</span>;
  const passing = pr.checksTotal - pr.checksFailing - pr.checksPending;
  return (
    <span className="checks" aria-label={`${passing} passing, ${pr.checksFailing} failing, ${pr.checksPending} running`}>
      <span className="check tone-good"><Icon name="check" size={12} />{passing}</span>
      {pr.checksFailing > 0 && <span className="check tone-critical"><Icon name="x" size={12} />{pr.checksFailing}</span>}
      {pr.checksPending > 0 && <span className="check tone-info"><Icon name="clock" size={12} />{pr.checksPending}</span>}
    </span>
  );
}

const REVIEW: Record<string, string> = {
  APPROVED: "Approved",
  CHANGES_REQUESTED: "Changes requested",
  REVIEW_REQUIRED: "Review required",
};

export function PrsPage() {
  const [params, setParams] = useSearchParams();
  const group = (params.get("group") as PrGroup | null) ?? "all-live";
  const [query, setQuery] = useState("");
  const q = useDeferredValue(query.trim().toLowerCase());
  const prs = useQuery({ queryKey: ["prs", "all"], queryFn: () => api.prs(true) });

  const counts = new Map<string, number>();
  for (const pr of prs.data ?? []) {
    const g = PR_STATE[pr.state].group;
    counts.set(g, (counts.get(g) ?? 0) + 1);
  }
  const live = (prs.data ?? []).filter((p) => PR_STATE[p.state].group !== "done").length;

  const rows = (prs.data ?? [])
    .filter((p) => (group === "all-live" ? PR_STATE[p.state].group !== "done" : PR_STATE[p.state].group === group))
    .filter((p) => !q || `${p.repo} ${p.number} ${p.title}`.toLowerCase().includes(q));

  const tabs = [{ key: "all-live", label: "All open", n: live }, ...PR_GROUPS.map((g) => ({ key: g.key, label: g.label, n: counts.get(g.key) ?? 0 }))];

  return (
    <>
      <PageHead title="Pull requests" sub="Every pull request the pipeline has opened, and where each one stands." />

      <div className="toolbar">
        <div className="segmented" role="tablist" aria-label="Filter by state">
          {tabs.map((t) => (
            <button
              key={t.key}
              type="button"
              role="tab"
              aria-selected={group === t.key}
              className="segment"
              onClick={() => setParams(t.key === "all-live" ? {} : { group: t.key }, { replace: true })}
            >
              {t.label}
              <span className="segment-count">{t.n}</span>
            </button>
          ))}
        </div>
        <label className="search">
          <Icon name="search" size={14} />
          <span className="sr-only">Search pull requests</span>
          <input type="search" placeholder="Search repo or title" value={query} onChange={(e) => setQuery(e.target.value)} />
        </label>
      </div>

      {prs.isPending ? (
        <Skeleton rows={6} />
      ) : prs.isError ? (
        <Failed error={prs.error} retry={() => prs.refetch()} />
      ) : rows.length === 0 ? (
        <Empty icon="pr" title={q ? "No pull requests match" : "Nothing in this group"}>
          {q ? "Try a different search." : PR_GROUPS.find((g) => g.key === group)?.hint}
        </Empty>
      ) : (
        <div className="table-wrap">
          <table className="table table-cards">
            <thead>
              <tr>
                <th scope="col">State</th>
                <th scope="col">Pull request</th>
                <th scope="col">Checks</th>
                <th scope="col">Review</th>
                <th scope="col" className="num">Diff</th>
                <th scope="col">Last activity</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((pr) => (
                <tr key={pr.id} className="row-link">
                  <td className="c-state"><PrStatePill state={pr.state} /></td>
                  <td className="cell-title">
                    <Link to={`/prs/${pr.id}`} className="row-anchor">{pr.title}</Link>
                    <span className="cell-sub mono">
                      {pr.repo}#{pr.number}
                      {pr.openFeedback > 0 && (
                        <span className="tag tag-warn"><Icon name="message" size={12} /> {pr.openFeedback} to answer</span>
                      )}
                    </span>
                  </td>
                  <td className="c-checks"><Checks pr={pr} /></td>
                  <td className="c-review">{pr.reviewDecision ? REVIEW[pr.reviewDecision] ?? pr.reviewDecision : <span className="muted">—</span>}</td>
                  <td className="num mono c-diff">
                    {pr.additions === null ? "—" : (
                      <><span className="add">+{pr.additions}</span> <span className="del">−{pr.deletions}</span></>
                    )}
                  </td>
                  <td className="c-time"><Time iso={pr.mergedAt ?? pr.closedAt ?? pr.remoteUpdatedAt ?? pr.openedAt} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
