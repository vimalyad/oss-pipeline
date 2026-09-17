"""The human gate. Nothing reaches implementation without passing through here."""

from __future__ import annotations

import yaml

from . import audit, policy, store
from .models import Status, transition


def approve(slug: str) -> str:
    cand = store.load(slug)
    if cand.status is not Status.PROPOSED:
        return f"{slug} is {cand.status}, not proposed — nothing to approve"
    transition(cand, Status.APPROVED, note="human approval")
    store.save(cand)
    audit.record("approve", slug=slug, detail=cand.url)
    return f"approved {slug} ({cand.repo}#{cand.issue})"


def reject(slug: str, reason: str) -> str:
    cand = store.load(slug)
    if cand.status in (Status.REJECTED, Status.ABANDONED):
        return f"{slug} is already {cand.status}"
    cand.reject_reason = reason
    transition(cand, Status.REJECTED, note=f"human rejection: {reason}")
    store.save(cand)
    audit.record("reject", slug=slug, detail=reason)
    return f"rejected {slug}: {reason}"


def exclude(repo: str) -> str:
    """Take a repo off the table -- e.g. before you work on it yourself from
    the Scaler account."""
    path = policy.CONFIG / "exclusions.yaml"
    data = yaml.safe_load(path.read_text()) or {}
    current = data.get("exclude_repos") or []
    if repo in current:
        return f"{repo} already excluded"
    data["exclude_repos"] = sorted(current + [repo])
    path.write_text(yaml.safe_dump(data, sort_keys=False))
    audit.record("exclude_repo", detail=repo)

    # Drop anything already queued for that repo so it cannot slip through.
    dropped = 0
    for cand in store.all_candidates():
        if cand.repo == repo and cand.status in (
            Status.DISCOVERED, Status.SCORED, Status.PROPOSED, Status.APPROVED
        ):
            cand.reject_reason = f"{repo} manually excluded"
            try:
                transition(cand, Status.REJECTED, note="repo excluded")
            except Exception:
                continue
            store.save(cand)
            dropped += 1
    return f"excluded {repo}" + (f", dropped {dropped} queued candidate(s)" if dropped else "")


def cla_signed(repo: str) -> str:
    """Record that you have signed this repo's CLA, clearing the blocker."""
    path = policy.CONFIG / "exclusions.yaml"
    data = yaml.safe_load(path.read_text()) or {}
    current = data.get("cla_signed") or []
    if repo in current:
        return f"{repo} already recorded as signed"
    data["cla_signed"] = sorted(current + [repo])
    path.write_text(yaml.safe_dump(data, sort_keys=False))
    audit.record("cla_signed", detail=repo)

    cleared = 0
    for cand in store.all_candidates():
        if cand.repo == repo and cand.blockers:
            cand.blockers = [b for b in cand.blockers if "CLA" not in b]
            store.save(cand)
            cleared += 1
    return f"recorded CLA for {repo}" + (f", unblocked {cleared} candidate(s)" if cleared else "")
