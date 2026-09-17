"""A single-writer lock for mutating runs.

The daily launchd job and any hourly check both call `watch --execute`. If they
overlap they would both clone, patch and push the same branch -- at best a
rejected push, at worst two divergent fixes for one review comment. Only one
mutating run at a time.

Stale locks are reclaimed: a crashed run must not wedge the pipeline until
someone notices.
"""

from __future__ import annotations

import json
import os
import time
from contextlib import contextmanager
from pathlib import Path

from .identity import ROOT

LOCK = ROOT / "state" / "pipeline.lock"
STALE_AFTER = 4 * 3600      # longer than any plausible implement run


class Busy(RuntimeError):
    """Another mutating run holds the lock."""


def _alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def holder() -> dict | None:
    if not LOCK.exists():
        return None
    try:
        info = json.loads(LOCK.read_text())
    except (json.JSONDecodeError, OSError):
        return None
    if not _alive(int(info.get("pid", -1))):
        return None
    if time.time() - float(info.get("at", 0)) > STALE_AFTER:
        return None
    return info


@contextmanager
def exclusive(what: str, *, required: bool = True):
    """Hold the lock for the duration of a mutating run."""
    current = holder()
    if current:
        msg = (f"another run holds the lock: {current.get('what')} "
               f"(pid {current.get('pid')}, started "
               f"{time.strftime('%H:%M:%S', time.localtime(current.get('at', 0)))})")
        if required:
            raise Busy(msg)
        yield False
        return

    LOCK.parent.mkdir(parents=True, exist_ok=True)
    LOCK.write_text(json.dumps({"pid": os.getpid(), "what": what, "at": time.time()}))
    try:
        yield True
    finally:
        try:
            info = json.loads(LOCK.read_text())
            if int(info.get("pid", -1)) == os.getpid():
                LOCK.unlink()
        except (json.JSONDecodeError, OSError, ValueError):
            pass
