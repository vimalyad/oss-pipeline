"""Caps, cooldowns and exclusions. Loaded fresh each run so edits take effect
without a restart."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from functools import cache
from pathlib import Path
from typing import Any

import yaml

from .identity import ROOT
from .models import Candidate, Status

CONFIG = ROOT / "config"
OPEN_STATUSES = {
    Status.PUSHED, Status.PR_OPEN, Status.CHANGES_REQUESTED, Status.UPDATING,
}


def _load(name: str) -> dict[str, Any]:
    return yaml.safe_load((CONFIG / name).read_text()) or {}


def policy() -> dict[str, Any]:
    return _load("policy.yaml")


def watchlist() -> dict[str, Any]:
    return _load("watchlist.yaml")


def exclusions() -> dict[str, Any]:
    return _load("exclusions.yaml")


def active_repos() -> list[str]:
    """Flattened tier-1 repos (plus tier 2 if unlocked), minus exclusions."""
    wl = watchlist()
    repos: list[str] = []
    for group in wl.get("tier1", {}).values():
        repos.extend(group)
    if policy().get("watchlist", {}).get("tier", 1) >= 2:
        repos.extend(wl.get("tier2", []))

    excl = exclusions()
    blocked = set(excl.get("exclude_repos") or []) | set(excl.get("banned_ai_policy") or [])
    return [r for r in repos if r not in blocked]


def labels() -> list[str]:
    return watchlist().get("labels", [])


class CapBreach(RuntimeError):
    """A cap would be exceeded. Not an error condition -- the expected way a
    low-volume pipeline says 'enough for today'."""


def check_caps(cands: list[Candidate], target: Candidate) -> list[str]:
    """Reasons `target` may not proceed to a PR right now. Empty means clear."""
    caps = policy()["caps"]
    reasons: list[str] = []
    now = datetime.now(timezone.utc)

    open_cands = [c for c in cands if c.status in OPEN_STATUSES]
    if len(open_cands) >= caps["max_open_prs"]:
        reasons.append(f"{len(open_cands)} PRs already open (cap {caps['max_open_prs']})")

    same_repo = [c for c in open_cands if c.repo == target.repo]
    if len(same_repo) >= caps["max_open_per_repo"]:
        reasons.append(f"already have an open PR on {target.repo}")

    today = now.date().isoformat()
    opened_today = [
        c for c in cands
        if any(h["to"] == Status.PR_OPEN and h["at"].startswith(today) for h in c.history)
    ]
    if len(opened_today) >= caps["prs_per_day"]:
        reasons.append(f"{len(opened_today)} PRs opened today (cap {caps['prs_per_day']})")

    cooldown = timedelta(days=caps["org_cooldown_days"])
    for c in cands:
        if c.owner != target.owner or c.slug == target.slug:
            continue
        for h in c.history:
            if h["to"] != Status.PR_OPEN:
                continue
            when = datetime.fromisoformat(h["at"])
            if now - when < cooldown:
                reasons.append(
                    f"org {target.owner} in cooldown until "
                    f"{(when + cooldown).date().isoformat()} (PR #{c.pr_number or '?'})"
                )
                break
    return reasons
