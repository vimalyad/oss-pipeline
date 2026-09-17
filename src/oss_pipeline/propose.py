"""The daily report -- the human gate's only input.

Every claim links back to the comment it came from, so the reading can be
checked rather than trusted. That matters because the brief is LLM-extracted and
a misread thread is the failure mode most likely to produce a bad PR.
"""

from __future__ import annotations

from datetime import date

from . import policy
from .identity import ROOT
from .models import Candidate, Contest, Status

REPORTS = ROOT / "reports"


def _brief_block(cand: Candidate) -> list[str]:
    b = cand.brief
    if not b:
        return ["- _no brief extracted_"]
    out: list[str] = []
    if b.maintainer_desired_approach:
        src = f" ([source]({b.approach_source_url}))" if b.approach_source_url else ""
        out.append(
            f"- **Maintainer wants** [{b.approach_author_association}]{src}: "
            f"\"{b.maintainer_desired_approach[:400]}\""
        )
    for r in b.rejected_approaches:
        out.append(f"- **Ruled out**: \"{r[:250]}\"")
    for a in b.acceptance_criteria:
        out.append(f"- **Done when**: {a[:250]}")
    for q in b.open_questions:
        out.append(f"- **UNRESOLVED**: {q[:250]}")
    if b.claimed_by:
        out.append(f"- **Claimed by** @{b.claimed_by} {b.claimed_at}")
    if b.reproduction:
        out.append(f"- **Repro**: {b.reproduction[:250]}")
    return out or ["- _brief is empty -- thread said nothing extractable_"]


def _policy_line(cand: Candidate) -> str:
    f = cand.facts
    if not f:
        return "policy: unknown"
    bits = [f"{f.stars:,}★", f.primary_language or "?"]
    if cand.issue_created_at:
        bits.insert(0, f"opened {cand.issue_created_at[:7]}")
    bits.append("DCO" if f.requires_dco else "no DCO")
    if f.requires_cla:
        bits.append("**CLA required**")
    if f.bans_ai_prs:
        bits.append("**BANS AI PRs**")
    elif f.requires_ai_disclosure:
        bits.append("AI disclosure required (goes in PR body only)")
    if not f.has_tests:
        bits.append("no tests")
    return " · ".join(bits)


def render(cands: list[Candidate], *, day: str | None = None) -> str:
    day = day or date.today().isoformat()
    approved = sorted(
        (c for c in cands if c.status is Status.PROPOSED),
        key=lambda c: (bool(c.blockers), len(c.soft_penalties), -c.reactions),
    )
    rejected = [c for c in cands if c.status is Status.REJECTED]
    comment_ops = [
        c for c in rejected
        if c.contest is Contest.ACTIVE_PR and c.pr_signal
    ]

    caps = policy.policy()["caps"]
    L: list[str] = [
        f"# Contribution proposals — {day}",
        "",
        f"{len(approved)} proposed · {len(rejected)} rejected · "
        f"caps: {caps['prs_per_day']}/day, {caps['max_open_prs']} open, "
        f"{caps['max_open_per_repo']}/repo",
        "",
        "Approve only what you could defend in a review conversation with the "
        "maintainer. Every quote below links to its source comment — check the "
        "reading, don't trust it.",
        "",
    ]

    if approved:
        L += ["## Proposed", ""]
        for c in approved:
            L += [
                f"### {c.repo}#{c.issue} — {c.title}",
                "",
                f"{c.url} · `{c.contest}` · {_policy_line(c)}",
                "",
            ]
            for b in c.blockers:
                L += [f"> **Blocked:** {b}", ""]
            if c.soft_penalties:
                L += [f"_Ranked lower: {'; '.join(c.soft_penalties)}_", ""]
            if c.contest is Contest.STALE_PR and c.pr_signal:
                s = c.pr_signal
                L += [
                    f"**Taking over a stale PR**: [#{s.number}]({s.url}) by @{s.author} "
                    f"— {'; '.join(s.reasons)}.",
                    f"Credit @{s.author} in the PR body and build on their commits "
                    f"where usable.",
                    "",
                ]
            L += _brief_block(c)
            L += [
                "",
                f"```\npipeline approve {c.slug}\n"
                f"pipeline reject  {c.slug} --reason \"...\"\n```",
                "",
            ]
    else:
        L += ["## Proposed", "", "_Nothing cleared every bar today._", ""]

    if comment_ops:
        L += [
            "## Comment opportunities",
            "",
            "Active PRs where a constructive comment may help. **No competing "
            "PR** — these are someone else's live work.",
            "",
        ]
        for c in comment_ops:
            s = c.pr_signal
            L += [f"- {c.repo}#{c.issue} → [PR #{s.number}]({s.url}) by @{s.author} "
                  f"({'; '.join(s.reasons)})"]
        L += [""]

    if rejected:
        L += ["## Rejected", "",
              "Reasons are the tuning signal — if a class of rejection looks "
              "wrong, the scorer or the watchlist is what needs changing.", ""]
        for c in rejected:
            why = c.reject_reason or "; ".join(c.score_failures[:3])
            L += [f"- **{c.repo}#{c.issue}** — {why}"]
        L += [""]

    return "\n".join(L)


def write(cands: list[Candidate], *, day: str | None = None):
    day = day or date.today().isoformat()
    REPORTS.mkdir(parents=True, exist_ok=True)
    p = REPORTS / f"{day}.md"
    p.write_text(render(cands, day=day))
    return p
