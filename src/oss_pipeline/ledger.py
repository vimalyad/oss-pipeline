"""The contribution record -- the actual point of the exercise.

Merge rate is also the control signal for widening the watchlist: breadth is
earned by merged work, not granted by time passing.
"""

from __future__ import annotations

import json
from datetime import datetime, timezone

from . import policy, store
from .identity import ROOT
from .models import Status

OUT = ROOT / "reports" / "ledger.md"
DATA = ROOT / "state" / "ledger.json"


def compute() -> dict:
    cands = store.all_candidates()
    merged = [c for c in cands if c.status is Status.MERGED]
    closed = [c for c in cands if c.status is Status.CLOSED]
    open_prs = [c for c in cands if c.status in policy.OPEN_STATUSES]

    decided = len(merged) + len(closed)
    rate = (len(merged) / decided) if decided else 0.0
    cfg = policy.policy()["watchlist"]
    return {
        "generated": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "merged": [
            {"repo": c.repo, "issue": c.issue, "pr": c.pr_url, "title": c.title,
             "stars": c.facts.stars if c.facts else 0,
             "language": c.facts.primary_language if c.facts else "",
             "credits": c.credits}
            for c in merged
        ],
        "counts": {"merged": len(merged), "closed": len(closed),
                   "open": len(open_prs), "tracked": len(cands)},
        "merge_rate": round(rate, 3),
        "tier2_unlocked": decided >= cfg["unlock_min_prs"] and rate >= cfg["unlock_merge_rate"],
        "tier2_needs": f"{cfg['unlock_min_prs']} decided PRs at >={cfg['unlock_merge_rate']:.0%}",
    }


def write() -> str:
    d = compute()
    DATA.parent.mkdir(parents=True, exist_ok=True)
    DATA.write_text(json.dumps(d, indent=2))

    L = [
        "# Contribution ledger", "",
        f"_{d['generated']}_", "",
        f"**{d['counts']['merged']} merged** · {d['counts']['closed']} closed · "
        f"{d['counts']['open']} open · {d['counts']['tracked']} tracked", "",
        f"Merge rate **{d['merge_rate']:.0%}**. "
        f"Tier 2 {'UNLOCKED' if d['tier2_unlocked'] else 'locked'} "
        f"(needs {d['tier2_needs']}).", "",
    ]
    if d["merged"]:
        L += ["## Merged", ""]
        for m in d["merged"]:
            credit = f" (with @{m['credits']})" if m["credits"] else ""
            L.append(f"- [{m['repo']}#{m['issue']}]({m['pr']}) — {m['title']}"
                     f" · {m['stars']:,}★ {m['language']}{credit}")
        L += ["", "### Resume form", "",
              "```", *[f"{m['repo']} ({m['stars']:,}★) — {m['title']} — {m['pr']}"
                       for m in d["merged"]], "```", ""]
    else:
        L += ["_No merged PRs yet._", ""]

    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("\n".join(L))
    return str(OUT)
