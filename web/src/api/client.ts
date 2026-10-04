import type {
  AuditEntry, CandidateDetail, CandidateStatus, CandidateSummary, Decision,
  FunnelStage, NeedsYouItem, Overview, PrCard, PrDetail, Week,
} from "./types";

/**
 * An API failure, carrying the server's own sentence. The API answers every
 * error as an RFC 9457 problem whose `detail` is written for a person ("x is
 * approved, not awaiting approval"), so that is what the UI shows.
 */
export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { Accept: "application/json", ...(init?.body ? { "Content-Type": "application/json" } : {}) },
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const problem = (await res.json()) as { detail?: string; title?: string };
      message = problem.detail ?? problem.title ?? message;
    } catch {
      // Not a problem document (a proxy error page, say); keep the status line.
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export const api = {
  overview: () => request<Overview>("/api/overview"),
  needsYou: () => request<NeedsYouItem[]>("/api/needs-you"),
  throughput: (weeks = 12) => request<Week[]>(`/api/throughput?weeks=${weeks}`),
  funnel: () => request<FunnelStage[]>("/api/funnel"),
  audit: (limit = 200) => request<AuditEntry[]>(`/api/audit?limit=${limit}`),

  prs: (all: boolean) => request<PrCard[]>(`/api/prs?all=${all}`),
  pr: (id: number) => request<PrDetail>(`/api/prs/${id}`),

  candidates: (statuses: CandidateStatus[] = []) => {
    const q = new URLSearchParams();
    for (const s of statuses) q.append("status", s);
    const qs = q.toString();
    return request<CandidateSummary[]>(`/api/candidates${qs ? `?${qs}` : ""}`);
  },
  candidate: (slug: string) => request<CandidateDetail>(`/api/candidates/${encodeURIComponent(slug)}`),

  approve: (slug: string, note?: string) =>
    request<Decision>(`/api/candidates/${encodeURIComponent(slug)}/approve`, {
      method: "POST",
      body: JSON.stringify({ note: note ?? null }),
    }),
  reject: (slug: string, reason: string) =>
    request<Decision>(`/api/candidates/${encodeURIComponent(slug)}/reject`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    }),
};
