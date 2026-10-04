import type { CandidateStatus, PrState } from "../api/types";
import type { IconName } from "../components/Icon";

/**
 * Every state carries a label and an icon as well as a tone, so meaning never
 * rides on colour alone (WCAG 1.4.1): a red pill and an amber one must still
 * read differently to someone who cannot tell red from amber.
 */
export type Tone = "good" | "warning" | "serious" | "critical" | "info" | "neutral" | "muted";

export interface StateMeta {
  label: string;
  tone: Tone;
  icon: IconName;
}

/** Board groups, in the order a person should deal with them. */
export type PrGroup = "attention" | "waiting" | "stale" | "done";

export const PR_STATE: Record<PrState, StateMeta & { group: PrGroup }> = {
  ci_failing: { label: "CI failing", tone: "critical", icon: "x-circle", group: "attention" },
  blocked_needs_human: { label: "Blocked", tone: "critical", icon: "hand", group: "attention" },
  conflicted: { label: "Conflicted", tone: "serious", icon: "split", group: "attention" },
  changes_requested: { label: "Changes requested", tone: "warning", icon: "message", group: "attention" },
  updating: { label: "Updating", tone: "info", icon: "refresh", group: "waiting" },
  approved: { label: "Approved", tone: "good", icon: "check-circle", group: "waiting" },
  ci_pending: { label: "CI running", tone: "info", icon: "clock", group: "waiting" },
  under_review: { label: "Under review", tone: "info", icon: "eye", group: "waiting" },
  review_required: { label: "Awaiting review", tone: "neutral", icon: "eye", group: "waiting" },
  open: { label: "Open", tone: "neutral", icon: "pr", group: "waiting" },
  draft: { label: "Draft", tone: "muted", icon: "pr", group: "waiting" },
  stale: { label: "Stale", tone: "muted", icon: "pause", group: "stale" },
  merged: { label: "Merged", tone: "good", icon: "merge", group: "done" },
  closed: { label: "Closed", tone: "muted", icon: "pr-closed", group: "done" },
};

export const PR_GROUPS: { key: PrGroup; label: string; hint: string }[] = [
  { key: "attention", label: "Needs attention", hint: "CI red, changes asked for, conflicts" },
  { key: "waiting", label: "With maintainers", hint: "Open and moving, nothing for you to do" },
  { key: "stale", label: "Stale", hint: "Nothing has happened for a long time" },
  { key: "done", label: "Finished", hint: "Merged or closed" },
];

export const CANDIDATE_STATUS: Record<CandidateStatus, StateMeta> = {
  discovered: { label: "Discovered", tone: "muted", icon: "search" },
  scored: { label: "Scored", tone: "muted", icon: "gauge" },
  proposed: { label: "Awaiting you", tone: "warning", icon: "inbox" },
  approved: { label: "Approved", tone: "info", icon: "check" },
  auto_approved: { label: "Auto-approved", tone: "info", icon: "bolt" },
  rejected: { label: "Rejected", tone: "muted", icon: "ban" },
  implementing: { label: "Implementing", tone: "info", icon: "code" },
  implemented: { label: "Implemented", tone: "info", icon: "code" },
  abandoned: { label: "Abandoned", tone: "serious", icon: "alert" },
  pushed: { label: "Pushed", tone: "info", icon: "upload" },
  pr_open: { label: "PR open", tone: "neutral", icon: "pr" },
  changes_requested: { label: "Changes requested", tone: "warning", icon: "message" },
  updating: { label: "Updating", tone: "info", icon: "refresh" },
  merged: { label: "Merged", tone: "good", icon: "merge" },
  closed: { label: "Closed", tone: "muted", icon: "pr-closed" },
  stale: { label: "Stale", tone: "muted", icon: "pause" },
};

/** The statuses a person can still act on from the dashboard. */
export const DECIDABLE: CandidateStatus[] = ["proposed"];
/** Statuses a rejection is still a legal edge from (mirrors allowed_transitions). */
export const REJECTABLE: CandidateStatus[] = [
  "discovered", "scored", "proposed", "approved", "auto_approved", "rejected",
];
