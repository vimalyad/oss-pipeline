"""Phase B: is this issue already being worked on?

The bias here is deliberately conservative: a PR is treated as ACTIVE unless it
is *provably* abandoned. Opening a competing PR against someone's live work is
the single most reliably resented thing an outside contributor can do, and the
cost lands on the user's account, so ambiguity resolves to "leave it alone".

Nothing here judges the quality of anyone's code. Classification uses dates,
labels and review states only. Quality ranking exists solely to prioritise
*within* the stale bucket -- never to justify contesting live work.
"""

from __future__ import annotations

from datetime import datetime, timezone

from . import ghapi, policy
from .models import Candidate, Contest, PRSignal

PR_DETAIL = """
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      number url isDraft state createdAt updatedAt
      author { login }
      labels(first:20) { nodes { name } }
      commits(last:1) {
        nodes { commit { committedDate statusCheckRollup { state } } }
      }
      comments(last:30) { nodes { createdAt author { login } } }
      reviews(last:30) { nodes { state createdAt author { login } } }
    }
  }
}
"""

STALE_LABEL_HINTS = ("stale", "abandoned", "inactive", "no-response", "needs-rebase")


def _days_since(iso: str | None) -> int | None:
    if not iso:
        return None
    when = datetime.fromisoformat(iso.replace("Z", "+00:00"))
    return (datetime.now(timezone.utc) - when).days


def _signal(repo: str, number: int) -> PRSignal:
    owner, name = repo.split("/")
    pr = ghapi.graphql(PR_DETAIL, owner=owner, name=name, number=number)[
        "repository"]["pullRequest"]

    author = (pr.get("author") or {}).get("login", "")
    commit_nodes = pr["commits"]["nodes"]
    commit = commit_nodes[0]["commit"] if commit_nodes else {}
    rollup = (commit.get("statusCheckRollup") or {}).get("state")

    author_comments = [
        c["createdAt"] for c in pr["comments"]["nodes"]
        if (c.get("author") or {}).get("login") == author
    ]
    changes_requested = [
        r["createdAt"] for r in pr["reviews"]["nodes"]
        if r.get("state") == "CHANGES_REQUESTED"
    ]
    labels = [l["name"].lower() for l in pr["labels"]["nodes"]]

    return PRSignal(
        number=pr["number"],
        url=pr["url"],
        author=author,
        is_draft=pr["isDraft"],
        days_since_commit=_days_since(commit.get("committedDate")),
        days_since_author_comment=_days_since(max(author_comments) if author_comments else None),
        days_since_changes_requested=_days_since(max(changes_requested) if changes_requested else None),
        has_stale_label=any(h in l for l in labels for h in STALE_LABEL_HINTS),
        checks_failing=rollup in ("FAILURE", "ERROR"),
    )


def _classify_one(sig: PRSignal) -> tuple[Contest, list[str]]:
    st = policy.policy()["staleness"]
    reasons: list[str] = []

    # Any recent author activity => live work. Checked first and wins outright.
    recent = [
        d for d in (sig.days_since_commit, sig.days_since_author_comment)
        if d is not None and d <= st["active_pr_days"]
    ]
    if recent:
        return Contest.ACTIVE_PR, [
            f"author active {min(recent)}d ago (within {st['active_pr_days']}d window)"
        ]

    if sig.has_stale_label:
        reasons.append("carries a stale/abandoned label")
    if (sig.days_since_commit or 0) > st["author_silent_days"]:
        reasons.append(f"no commit for {sig.days_since_commit}d")
    if (sig.days_since_changes_requested or 0) > st["changes_requested_days"]:
        unanswered = (
            sig.days_since_author_comment is None
            or sig.days_since_author_comment > sig.days_since_changes_requested
        )
        if unanswered:
            reasons.append(
                f"changes requested {sig.days_since_changes_requested}d ago, unanswered"
            )
    if sig.checks_failing and (sig.days_since_commit or 0) > st["ci_red_untouched_days"]:
        reasons.append(f"CI red and untouched for {sig.days_since_commit}d")

    if reasons:
        return Contest.STALE_PR, reasons

    # Between the windows with no positive staleness signal: do not contest.
    return Contest.ACTIVE_PR, [
        "no activity in the active window, but no staleness signal either "
        "-- treated as live (conservative default)"
    ]


def classify(cand: Candidate) -> tuple[Contest, PRSignal | None]:
    prs = getattr(cand, "_linked_prs", None)
    if prs is None:
        prs = []
    if not prs:
        return Contest.NO_PR, None

    signals = []
    for pr in prs:
        try:
            signals.append(_signal(cand.repo, pr["number"]))
        except ghapi.GhError as exc:
            # Unknown state is not evidence of abandonment.
            print(f"    {cand.slug}: PR #{pr['number']} unreadable ({str(exc)[:50]})"
                  f" -- treating issue as contested")
            return Contest.ACTIVE_PR, None

    verdicts = [_classify_one(s) for s in signals]
    # One live PR is enough to make the issue off limits.
    for (verdict, reasons), sig in zip(verdicts, signals):
        if verdict is Contest.ACTIVE_PR:
            sig.reasons = reasons
            return Contest.ACTIVE_PR, sig

    # All stale: surface the one that is most abandoned (longest silence).
    best = max(
        zip(signals, verdicts),
        key=lambda pair: pair[0].days_since_commit or 0,
    )
    best[0].reasons = best[1][1]
    return Contest.STALE_PR, best[0]
