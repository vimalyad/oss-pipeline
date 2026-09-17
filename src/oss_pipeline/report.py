"""A single human-readable status page, regenerated on every watch cycle.

The pipeline already writes proposal reports and a ledger, but neither answers
the question actually being asked day to day: what is happening with my open
PRs, and is anything waiting on me? Logs do not answer it either -- they are
append-only and you have to reconstruct the present from them.
"""

from __future__ import annotations

from datetime import datetime, timezone

from . import ghapi, policy, replies, store
from .identity import ROOT, load_identity
from .models import Status

OUT = ROOT / "reports" / "STATUS.md"

PR_QUERY = """
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      state merged isDraft mergeable updatedAt url title
      reviewDecision
      commits(last:1) { nodes { commit { statusCheckRollup {
        state contexts(first:100) { nodes {
          __typename
          ... on CheckRun { name conclusion }
          ... on StatusContext { context state }
        } } } } } }
      reviews(last:10) { nodes { state author { login } submittedAt } }
      comments(last:5) { nodes { author { login } createdAt } }
    }
  }
}
"""


def _pr_state(cand) -> dict:
    owner, name = cand.repo.split("/")
    pr = ghapi.graphql(PR_QUERY, owner=owner, name=name,
                       number=cand.pr_number)["repository"]["pullRequest"]
    nodes = pr["commits"]["nodes"]
    rollup = (nodes[0]["commit"].get("statusCheckRollup") or {}) if nodes else {}
    counts: dict[str, int] = {}
    failing: list[str] = []
    for ctx in (rollup.get("contexts", {}) or {}).get("nodes", []) or []:
        if ctx.get("__typename") == "CheckRun":
            key = (ctx.get("conclusion") or "pending").lower()
            if key in ("failure", "timed_out", "cancelled"):
                failing.append(ctx["name"])
        else:
            key = (ctx.get("state") or "pending").lower()
            if key in ("failure", "error"):
                failing.append(ctx.get("context", "?"))
        counts[key] = counts.get(key, 0) + 1
    return {"pr": pr, "counts": counts, "failing": failing}


def build() -> str:
    ident = load_identity()
    now = datetime.now(timezone.utc)
    L = [f"# Pipeline status", "", f"_generated {now:%Y-%m-%d %H:%M} UTC_", ""]

    live = store.by_status(*policy.OPEN_STATUSES)
    actions: list[str] = []

    L += ["## Open pull requests", ""]
    if not live:
        L += ["_none open_", ""]
    for cand in live:
        try:
            info = _pr_state(cand)
        except Exception as exc:
            L += [f"- **{cand.repo}#{cand.pr_number}** — could not read: {str(exc)[:80]}", ""]
            continue
        pr, counts = info["pr"], info["counts"]
        ci = ", ".join(f"{n} {k}" for k, n in sorted(counts.items())) or "no checks"
        decision = pr.get("reviewDecision") or "no review yet"
        L += [
            f"### [{cand.repo}#{cand.pr_number}]({pr['url']})",
            f"_{pr['title']}_", "",
            f"- state **{pr['state']}**"
            + (" · **MERGED**" if pr["merged"] else "")
            + f" · review: **{decision}** · mergeable: {pr.get('mergeable')}",
            f"- CI: {ci}",
        ]
        if info["failing"]:
            L.append(f"- **FAILING:** {', '.join(info['failing'][:6])}")
            actions.append(f"CI failing on {cand.repo}#{cand.pr_number}: "
                           f"{info['failing'][0]}")
        latest = (pr.get("comments", {}).get("nodes") or [])
        if latest:
            c = latest[-1]
            who = (c.get("author") or {}).get("login", "?")
            L.append(f"- last comment: **{who}** at {c['createdAt'][:16].replace('T', ' ')}")
            if who != ident.login:
                actions.append(f"{who} commented on {cand.repo}#{cand.pr_number}")
        pending = replies.pending(cand)
        if pending:
            drafted = sum(1 for p in pending if p.get("draft"))
            L.append(f"- **{len(pending)} item(s) awaiting your reply** "
                     f"({drafted} drafted) — `pipeline replies`")
            actions.append(f"{len(pending)} reply(ies) queued on {cand.repo}#{cand.pr_number}")
        L.append("")

    # Two different things, kept apart on purpose. A section headed "needs you"
    # that lists machine-queued work teaches you to skip the section, which is
    # exactly when a real request gets missed.
    proposed = store.by_status(Status.PROPOSED)
    approved = store.by_status(Status.APPROVED)
    everything = store.all_candidates()

    needs_you = []
    for c in proposed:
        if c.blockers:
            needs_you.append(
                (f"**{c.repo}#{c.issue}** — {c.blockers[0][:90]}", None))
        else:
            needs_you.append(
                (f"**{c.repo}#{c.issue}** {c.title[:58]} — approve or reject",
                 f"pipeline approve {c.slug}"))
    for c in approved:
        if c.blockers:
            needs_you.append((f"**{c.repo}#{c.issue}** — {c.blockers[0][:90]}", None))

    if needs_you:
        L += ["## Needs you", ""]
        for text, cmd in needs_you:
            L.append(f"- {text}")
            if cmd:
                L.append(f"  `{cmd}`")
        L.append("")

    queued = [c for c in approved if not c.blockers]
    if queued:
        L += ["## Queued — no action needed", ""]
        for c in queued:
            caps = policy.check_caps(everything, c)
            why = caps[0] if caps else "will run on the next scheduled cycle (09:30 daily)"
            L.append(f"- **{c.repo}#{c.issue}** {c.title[:52]}")
            L.append(f"  {why}")
        L.append("")

    if actions:
        L = L[:3] + ["## Needs attention", "",
                     *[f"- {a}" for a in actions], ""] + L[3:]
    else:
        L = L[:3] + ["_Nothing needs your attention._", ""] + L[3:]

    from . import ledger
    d = ledger.compute()
    L += [
        "## Totals", "",
        f"- merged **{d['counts']['merged']}** · closed {d['counts']['closed']} · "
        f"open {d['counts']['open']} · tracked {d['counts']['tracked']}",
        f"- merge rate {d['merge_rate']:.0%}",
        "",
    ]
    return "\n".join(L)


def write() -> str:
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(build())
    return str(OUT)
