"""Data shapes shared across stages, and the candidate state machine."""

from __future__ import annotations

import enum
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from typing import Any


class Status(enum.StrEnum):
    DISCOVERED = "discovered"
    SCORED = "scored"
    PROPOSED = "proposed"
    APPROVED = "approved"
    REJECTED = "rejected"
    IMPLEMENTING = "implementing"
    IMPLEMENTED = "implemented"
    ABANDONED = "abandoned"
    PUSHED = "pushed"
    PR_OPEN = "pr_open"
    CHANGES_REQUESTED = "changes_requested"
    UPDATING = "updating"
    MERGED = "merged"
    CLOSED = "closed"
    STALE = "stale"


# Explicit transitions: an unattended job must not be able to skip the human
# gate, so there is no edge from SCORED/PROPOSED straight to IMPLEMENTING.
TRANSITIONS: dict[Status, set[Status]] = {
    Status.DISCOVERED: {Status.SCORED, Status.REJECTED},
    Status.SCORED: {Status.PROPOSED, Status.REJECTED},
    Status.PROPOSED: {Status.APPROVED, Status.REJECTED},
    Status.APPROVED: {Status.IMPLEMENTING, Status.ABANDONED},
    Status.IMPLEMENTING: {Status.IMPLEMENTED, Status.ABANDONED},
    Status.IMPLEMENTED: {Status.PUSHED, Status.ABANDONED},
    # GitHub -- not this pipeline -- decides merge and close. Every status the
    # watcher can observe a PR in must therefore accept both outcomes, or
    # watch.sync raises and the result is swallowed. That cost us nothing yet
    # only because nothing has merged: a PR sat in CHANGES_REQUESTED while
    # watch.py called transition(MERGED), which this table forbade.
    Status.PUSHED: {Status.PR_OPEN, Status.ABANDONED, Status.MERGED, Status.CLOSED},
    Status.PR_OPEN: {Status.CHANGES_REQUESTED, Status.UPDATING, Status.MERGED,
                     Status.CLOSED, Status.STALE},
    # PR_OPEN is reachable again because a maintainer can dismiss their own
    # review, which returns the PR to plain open.
    Status.CHANGES_REQUESTED: {Status.UPDATING, Status.PR_OPEN, Status.MERGED,
                               Status.CLOSED, Status.STALE},
    Status.UPDATING: {Status.PR_OPEN, Status.ABANDONED, Status.MERGED,
                      Status.CLOSED, Status.STALE},
    Status.REJECTED: set(),
    Status.ABANDONED: set(),
    Status.MERGED: set(),
    Status.CLOSED: set(),
    Status.STALE: {Status.PR_OPEN, Status.UPDATING, Status.MERGED, Status.CLOSED},
}


class Contest(enum.StrEnum):
    """How contested an issue is. Decided by objective signals only."""

    NO_PR = "no_pr"
    STALE_PR = "stale_pr"
    ACTIVE_PR = "active_pr"
    CLAIMED = "claimed"


TARGETABLE = {Contest.NO_PR, Contest.STALE_PR}


@dataclass
class PRSignal:
    """Why an existing PR was judged active or abandoned. Auditable, not vibes."""

    number: int
    url: str
    author: str
    is_draft: bool
    days_since_commit: int | None
    days_since_author_comment: int | None
    days_since_changes_requested: int | None
    has_stale_label: bool
    checks_failing: bool
    reasons: list[str] = field(default_factory=list)


@dataclass
class Brief:
    """What the issue thread actually asks for. The patch spec."""

    maintainer_desired_approach: str = ""
    approach_source_url: str = ""
    approach_author_association: str = ""
    rejected_approaches: list[str] = field(default_factory=list)
    acceptance_criteria: list[str] = field(default_factory=list)
    open_questions: list[str] = field(default_factory=list)
    claimed_by: str = ""
    claimed_at: str = ""
    reproduction: str = ""


@dataclass
class RepoFacts:
    """Repo-level gates, cached: they change slowly and cost API calls."""

    repo: str
    stars: int = 0
    has_contributing: bool = False
    bans_ai_prs: bool = False
    requires_ai_disclosure: bool = False
    ai_policy_quote: str = ""
    requires_dco: bool = False
    requires_cla: bool = False
    has_tests: bool = False
    primary_language: str = ""
    merged_first_time_pr_90d: bool = False
    # Eligibility rules the project states for outside PRs. cli/cli accepts them
    # "only for issues labelled `help wanted`", and closed one of ours for that.
    required_issue_labels: list[str] = field(default_factory=list)
    forbidden_issue_labels: list[str] = field(default_factory=list)
    eligibility_quote: str = ""
    fetched_at: str = ""


@dataclass
class Candidate:
    repo: str
    issue: int
    title: str
    url: str
    status: Status = Status.DISCOVERED
    labels: list[str] = field(default_factory=list)
    comments: int = 0
    reactions: int = 0
    issue_updated_at: str = ""
    issue_created_at: str = ""
    contest: Contest | None = None
    pr_signal: PRSignal | None = None
    brief: Brief | None = None
    facts: RepoFacts | None = None
    score_failures: list[str] = field(default_factory=list)
    # Soft penalties do not reject; they rank a candidate below cleaner ones.
    soft_penalties: list[str] = field(default_factory=list)
    # Blockers need a one-off human action (signing a CLA) before implementing.
    blockers: list[str] = field(default_factory=list)
    reject_reason: str = ""
    branch: str = ""
    pr_number: int | None = None
    pr_url: str = ""
    watch_seen: list[str] = field(default_factory=list)
    queued_replies: list[dict[str, Any]] = field(default_factory=list)
    disclosed_ai: bool = False
    credits: str = ""
    history: list[dict[str, Any]] = field(default_factory=list)

    @property
    def slug(self) -> str:
        return f"{self.repo.replace('/', '__')}__{self.issue}"

    @property
    def owner(self) -> str:
        return self.repo.split("/")[0]

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


class TransitionError(RuntimeError):
    """An illegal state change. Almost always a bug in the runner's ordering."""


def transition(cand: Candidate, new: Status, note: str = "") -> None:
    allowed = TRANSITIONS.get(cand.status, set())
    if new not in allowed:
        raise TransitionError(
            f"{cand.slug}: cannot go {cand.status} -> {new} "
            f"(allowed: {sorted(allowed) or 'none, terminal'})"
        )
    cand.history.append({
        "at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "from": str(cand.status), "to": str(new), "note": note,
    })
    cand.status = new
