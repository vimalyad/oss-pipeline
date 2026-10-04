import { useQuery } from "@tanstack/react-query";
import { useDeferredValue, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { api } from "../api/client";
import type { CandidateStatus } from "../api/types";
import { Icon } from "../components/Icon";
import { PageHead } from "../components/Layout";
import { StatusPill } from "../components/Pill";
import { Empty, Failed, Skeleton } from "../components/States";
import { Time } from "../components/Time";

const VIEWS: { key: string; label: string; statuses: CandidateStatus[] }[] = [
  { key: "active", label: "In motion", statuses: [] },
  { key: "approved", label: "Approved", statuses: ["approved", "auto_approved", "implementing", "implemented"] },
  { key: "early", label: "Discovered", statuses: ["discovered", "scored"] },
  { key: "rejected", label: "Rejected", statuses: ["rejected"] },
  { key: "abandoned", label: "Abandoned", statuses: ["abandoned"] },
  { key: "finished", label: "Finished", statuses: ["merged", "closed"] },
];

export function CandidatesPage() {
  const [params, setParams] = useSearchParams();
  const view = VIEWS.find((v) => v.key === params.get("view")) ?? VIEWS[0]!;
  const [query, setQuery] = useState("");
  const q = useDeferredValue(query.trim().toLowerCase());
  const list = useQuery({ queryKey: ["candidates", view.statuses], queryFn: () => api.candidates(view.statuses) });
  const rows = (list.data ?? []).filter((c) => !q || `${c.repo} ${c.issueNumber} ${c.title}`.toLowerCase().includes(q));

  return (
    <>
      <PageHead title="Candidates" sub="Every issue the pipeline has looked at, wherever it got to." />
      <div className="toolbar">
        <div className="segmented" role="tablist" aria-label="Filter by stage">
          {VIEWS.map((v) => (
            <button
              key={v.key}
              type="button"
              role="tab"
              aria-selected={view.key === v.key}
              className="segment"
              onClick={() => setParams(v.key === "active" ? {} : { view: v.key }, { replace: true })}
            >
              {v.label}
            </button>
          ))}
        </div>
        <label className="search">
          <Icon name="search" size={14} />
          <span className="sr-only">Search candidates</span>
          <input type="search" placeholder="Search repo or title" value={query} onChange={(e) => setQuery(e.target.value)} />
        </label>
      </div>

      {list.isPending ? (
        <Skeleton rows={8} />
      ) : list.isError ? (
        <Failed error={list.error} retry={() => list.refetch()} />
      ) : rows.length === 0 ? (
        <Empty icon="list" title="No candidates here" />
      ) : (
        <div className="table-wrap">
          <table className="table table-long table-cards">
            <thead>
              <tr>
                <th scope="col">Status</th>
                <th scope="col">Issue</th>
                <th scope="col">{view.key === "rejected" ? "Why" : "Labels"}</th>
                <th scope="col">Updated</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.slug} className="row-link">
                  <td className="c-state"><StatusPill status={c.status} /></td>
                  <td className="cell-title">
                    <Link to={`/candidates/${c.slug}`} className="row-anchor">{c.title}</Link>
                    <span className="cell-sub mono">{c.repo}#{c.issueNumber}</span>
                  </td>
                  <td className="cell-why c-why">
                    {view.key === "rejected" ? (
                      <>
                        {c.rejectKind && <span className="tag">{c.rejectKind}</span>} {c.rejectReason?.replace(/^human rejection: /, "")}
                      </>
                    ) : (
                      c.labels.map((l) => <span key={l} className="tag">{l}</span>)
                    )}
                  </td>
                  <td className="c-time"><Time iso={c.updatedAt} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
