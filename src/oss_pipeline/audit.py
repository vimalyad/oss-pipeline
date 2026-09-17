"""Append-only record of everything this pipeline did.

The pipeline acts publicly under the user's name while unattended, so "what did
it do at 3am" must be answerable without re-deriving it from GitHub.
"""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from pathlib import Path

from .identity import ROOT

LOG = ROOT / "state" / "audit.log"


def record(action: str, *, slug: str = "", detail: str = "", **extra) -> None:
    LOG.parent.mkdir(parents=True, exist_ok=True)
    entry = {
        "at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "action": action,
        "slug": slug,
        "detail": detail,
        "pid": os.getpid(),
        **extra,
    }
    with LOG.open("a") as fh:
        fh.write(json.dumps(entry, default=str) + "\n")


def tail(n: int = 20) -> list[dict]:
    if not LOG.exists():
        return []
    lines = LOG.read_text().splitlines()[-n:]
    return [json.loads(l) for l in lines if l.strip()]
