"""Every bar a candidate must clear. All of them, not most of them.

Returns the full list of failures rather than short-circuiting: when the daily
report says a candidate was rejected, it should say everything that was wrong
with it, because that is what tunes the watchlist.
"""

from __future__ import annotations

import json
import re
from datetime import datetime, timedelta, timezone

from . import ghapi, policy, repofacts, store, toolchain
from .identity import ROOT, load_identity
from .models import Candidate, Contest, TARGETABLE

TOUCHED_CACHE = ROOT / "state" / "cross_account_repos.json"
CACHE_DAYS = 3

DOCS_ONLY = re.compile(
    r"\b(typo|spelling|grammar|broken link|dead link|readme|changelog)\b", re.I
)
DOCS_LABELS = {"documentation", "docs", "typo", "good first issue: docs"}


def repos_touched_by_other_accounts(*, refresh: bool = False) -> set[str]:
    """Repos the user's OTHER accounts have already contributed to.

    All three accounts share the display name "Vimal Kumar Yadav", so two of
    them contributing to one repo is trivially linkable and can read as
    sockpuppeting even when innocent.
    """
    if TOUCHED_CACHE.exists() and not refresh:
        raw = json.loads(TOUCHED_CACHE.read_text())
        age = datetime.now(timezone.utc) - datetime.fromisoformat(raw["fetched_at"])
        if age < timedelta(days=CACHE_DAYS):
            return set(raw["repos"])

    # Deliberately NOT from exclusions.yaml: that file is tracked by git, and
    # listing the user's other logins there links all three accounts together --
    # the exact outcome identity isolation exists to prevent. identity.env is
    # gitignored and already carries them.
    logins = list(load_identity().other_logins)
    query = """
    query($q:String!) {
      search(query:$q, type:ISSUE, first:100) {
        nodes {
          ... on PullRequest { repository { nameWithOwner } }
          ... on Issue { repository { nameWithOwner } }
        }
      }
    }
    """
    repos: set[str] = set()
    for login in logins:
        for kind in ("is:pr", "is:issue"):
            try:
                nodes = ghapi.graphql(query, q=f"author:{login} {kind}")["search"]["nodes"]
            except ghapi.GhError:
                continue
            for n in nodes:
                name = ((n or {}).get("repository") or {}).get("nameWithOwner")
                if name:
                    repos.add(name)

    TOUCHED_CACHE.parent.mkdir(parents=True, exist_ok=True)
    TOUCHED_CACHE.write_text(json.dumps({
        "fetched_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "logins": logins,
        "repos": sorted(repos),
    }, indent=2))
    return repos


def maintainer_accepted(cand: Candidate) -> tuple[bool, str]:
    """Did someone with triage rights accept this issue?

    Label-based, because outside contributors cannot apply labels: a
    `good first issue` label is a maintainer action by construction. A maintainer
    comment stating an approach also counts. An explicit untriaged label vetoes
    both -- `triage/pending` means precisely that nobody has decided yet.
    """
    cfg = policy.policy()
    accept = {l.lower() for l in cfg.get("acceptance_labels", [])}
    untriaged = {l.lower() for l in cfg.get("untriaged_labels", [])}
    have = {l.lower() for l in cand.labels}

    blocked = have & untriaged
    if blocked:
        return False, f"carries untriaged label {sorted(blocked)[0]!r}"

    hit = have & accept
    if hit:
        return True, f"maintainer applied {sorted(hit)[0]!r}"

    b = cand.brief
    if b and b.maintainer_desired_approach:
        return True, f"maintainer stated an approach ({b.approach_author_association})"

    return False, "no triage-gated label and no maintainer statement"


def score(cand: Candidate, *, touched: set[str] | None = None) -> tuple[bool, list[str]]:
    cfg = policy.policy()
    rules = cfg["scoring"]
    st = cfg["staleness"]
    fails: list[str] = []
    penalties: list[str] = []
    blockers: list[str] = []

    # --- contest ------------------------------------------------------------
    if cand.contest is None:
        fails.append("not classified (contest.py did not run)")
    elif cand.contest not in TARGETABLE:
        fails.append(f"contest class is {cand.contest} (targetable: no_pr, stale_pr)")

    # --- cross-account ------------------------------------------------------
    touched = repos_touched_by_other_accounts() if touched is None else touched
    if cand.repo in touched:
        ident = load_identity()
        fails.append(
            f"{cand.repo} already touched by another of your accounts "
            f"({', '.join(ident.other_logins)}) -- linkable, see plan section 7"
        )
    excl = policy.exclusions()
    if cand.repo in (excl.get("exclude_repos") or []):
        fails.append(f"{cand.repo} is manually excluded")

    # --- repo facts ---------------------------------------------------------
    facts = cand.facts or repofacts.facts(cand.repo)
    cand.facts = facts
    if facts.bans_ai_prs:
        fails.append(f"repo bans AI-assisted PRs: {facts.ai_policy_quote[:120]!r}")
    # Soft, not fatal: has_contributing is only a proxy for "accepts outside
    # contributions", and merged_first_time_pr_90d measures that directly. A repo
    # that demonstrably merges newcomer PRs without a CONTRIBUTING.md is fine --
    # it just ranks below one that documents its process.
    if not facts.has_contributing:
        penalties.append("no CONTRIBUTING.md (ranked lower)")
    if rules["require_tests"] and not facts.has_tests:
        fails.append("no test suite (CI cannot act as a correctness oracle)")
    if not facts.merged_first_time_pr_90d:
        fails.append(
            f"no PR from an outside contributor merged in "
            f"{rules['first_time_contributor_window_days']}d -- unreceptive"
        )
    missing = toolchain.missing_toolchain(facts.primary_language)
    if missing:
        blockers.append(
            f"{facts.primary_language} toolchain missing -- install `{missing}` "
            f"(e.g. brew install {missing}) before this can be built or tested"
        )

    if facts.requires_cla and cand.repo not in (excl.get("cla_signed") or []):
        blockers.append(
            f"CLA required for {cand.owner} -- sign once, then "
            f"`pipeline cla-signed {cand.repo}`"
        )

    # --- project eligibility rules ------------------------------------------
    have_labels = {l.lower() for l in cand.labels}
    missing = [r for r in (facts.required_issue_labels or []) if r not in have_labels]
    if missing:
        fails.append(
            f"{cand.repo} accepts outside PRs only for issues labelled "
            f"{missing[0]!r}; this issue has {sorted(have_labels)}"
        )
    banned = [f for f in (facts.forbidden_issue_labels or []) if f in have_labels]
    if banned:
        fails.append(f"{cand.repo} does not accept PRs for issues labelled {banned[0]!r}")

    # --- thread -------------------------------------------------------------
    b = cand.brief
    if b is None:
        fails.append("no brief (harvest/brief did not run)")
    else:
        accepted, why = maintainer_accepted(cand)
        if rules.get("require_maintainer_acceptance", True) and not accepted:
            fails.append(f"no maintainer acceptance ({why})")

        if rules.get("require_approach_or_criteria", True) and not (
            b.maintainer_desired_approach or b.acceptance_criteria
        ):
            fails.append(
                "no stated approach AND no acceptance criteria -- a patch here "
                "would be guesswork"
            )
        if rules["require_converged_thread"] and b.open_questions:
            fails.append(
                f"thread has not converged: {len(b.open_questions)} open question(s) "
                f"-- {b.open_questions[0][:90]!r}"
            )
        if b.claimed_by:
            recent = True
            if b.claimed_at:
                try:
                    when = datetime.fromisoformat(b.claimed_at.replace("Z", "+00:00"))
                    if when.tzinfo is None:
                        when = when.replace(tzinfo=timezone.utc)
                    recent = (datetime.now(timezone.utc) - when).days <= st["claim_honoured_days"]
                except ValueError:
                    pass
            if recent:
                fails.append(f"claimed by @{b.claimed_by} -- respect the claim")

    # --- age ----------------------------------------------------------------
    # A maintainer's stated approach ages with the codebase. datasets#2267 was
    # opened in 2021 and its diagnosis quotes code that has had five years to
    # move; the approach may simply no longer describe the repo. Not fatal --
    # plenty of good-first-issues sit for years -- but it ranks lower and the
    # report says how old it is so the reading can be checked.
    stale_years = rules.get("stale_issue_penalty_years", 2)
    if cand.issue_created_at:
        try:
            opened = datetime.fromisoformat(cand.issue_created_at.replace("Z", "+00:00"))
            age_days = (datetime.now(timezone.utc) - opened).days
            if age_days > stale_years * 365:
                penalties.append(
                    f"issue opened {age_days // 365}y ago ({opened:%Y-%m}); the stated "
                    f"approach may predate the current code"
                )
        except ValueError:
            pass

    # --- shape of the work --------------------------------------------------
    if rules["reject_docs_typo_only"]:
        label_set = {l.lower() for l in cand.labels}
        if DOCS_ONLY.search(cand.title) or (label_set & DOCS_LABELS and cand.comments <= 1):
            fails.append("looks docs/typo-only -- noise in a mature repo")

    cand.score_failures = fails
    cand.soft_penalties = penalties
    cand.blockers = blockers
    return (not fails), fails
