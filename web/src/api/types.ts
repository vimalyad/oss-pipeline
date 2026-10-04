// The API's shapes, mirroring the Java records in api/. Timestamps arrive as
// ISO-8601 strings; nullable columns are `| null` because in this schema NULL
// means "not observed yet", which must not render as zero.

export type CandidateStatus =
  | "discovered" | "scored" | "proposed" | "approved" | "auto_approved" | "rejected"
  | "implementing" | "implemented" | "abandoned" | "pushed" | "pr_open"
  | "changes_requested" | "updating" | "merged" | "closed" | "stale";

export type PrState =
  | "draft" | "open" | "review_required" | "under_review" | "changes_requested"
  | "approved" | "ci_failing" | "ci_pending" | "conflicted" | "updating" | "stale"
  | "blocked_needs_human" | "merged" | "closed";

export interface CandidateSummary {
  id: number;
  slug: string;
  repo: string;
  issueNumber: number;
  title: string;
  url: string;
  status: CandidateStatus;
  labels: string[];
  contest: string;
  rejectKind: string | null;
  rejectReason: string | null;
  blockers: string[];
  softPenalties: string[];
  scoreFailures: string[];
  issueCreatedAt: string | null;
  updatedAt: string;
  prId: number | null;
}

export interface HistoryEntry {
  from: CandidateStatus | null;
  to: CandidateStatus;
  note: string | null;
  forced: boolean;
  actor: string | null;
  at: string;
}

export interface Brief {
  maintainerApproach: string | null;
  approachSourceUrl: string | null;
  approachAssociation: string | null;
  rejectedApproaches: string[];
  acceptanceCriteria: string[];
  openQuestions: string[];
  claimedBy: string | null;
  reproduction: string | null;
  dropped: string[];
  extractedAt: string;
}

export interface Signal {
  prNumber: number;
  url: string | null;
  author: string | null;
  draft: boolean;
  daysSinceCommit: number | null;
  daysSinceAuthorComment: number | null;
  reviewed: boolean;
  checksFailing: boolean;
  reasons: string[];
}

export interface AuditEntry {
  id: number;
  action: string;
  slug: string | null;
  detail: string | null;
  actor: string | null;
  at: string;
}

export interface CandidateDetail {
  candidate: CandidateSummary;
  brief: Brief | null;
  signal: Signal | null;
  history: HistoryEntry[];
  audit: AuditEntry[];
}

export interface Decision {
  slug: string;
  status: CandidateStatus;
  changed: boolean;
  message: string;
}

export interface PrCard {
  id: number;
  number: number;
  repo: string;
  language: string | null;
  stars: number | null;
  slug: string;
  issueNumber: number;
  title: string;
  issueUrl: string;
  prUrl: string;
  state: PrState;
  checksTotal: number;
  checksFailing: number;
  checksPending: number;
  reviewDecision: string | null;
  reviewerCount: number;
  unansweredItems: number;
  additions: number | null;
  deletions: number | null;
  changedFiles: number | null;
  openedAt: string;
  remoteUpdatedAt: string | null;
  mergedAt: string | null;
  closedAt: string | null;
  idleDays: number | null;
  tookOver: boolean;
  credits: string | null;
  openFeedback: number;
}

export interface StateChange {
  from: PrState | null;
  to: PrState;
  detail: string | null;
  at: string;
}

export interface Feedback {
  id: number;
  author: string;
  authorAssociation: string | null;
  kind: string;
  body: string;
  url: string | null;
  classification: string | null;
  createdAt: string;
  draft: string | null;
  posted: boolean;
  postedUrl: string | null;
  postedAt: string | null;
}

export interface PrDetail {
  pr: PrCard;
  candidate: CandidateSummary | null;
  states: StateChange[];
  lifecycle: HistoryEntry[];
  feedback: Feedback[];
}

export interface Overview {
  caps: { prsPerDay: number; prsPerWeek: number; maxOpenPrs: number; maxOpenPerRepo: number };
  usage: { openedToday: number; openedThisWeek: number; open: number };
  needsYou: number;
  awaitingApproval: number;
  mergedAllTime: number;
}

export interface NeedsYouItem {
  what: "reply" | "blocker" | "proposal";
  refId: number;
  repo: string;
  issueNumber: number;
  title: string;
  link: string;
  author: string | null;
  since: string;
  slug: string | null;
  prId: number | null;
}

export interface Week {
  week: string;
  opened: number;
  merged: number;
  closed: number;
  proposed: number;
  approved: number;
}

export interface FunnelStage {
  stage: CandidateStatus;
  reached: number;
}
