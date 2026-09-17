"""GitHub access, always as the OSS identity.

Everything goes through `gh` rather than a raw HTTP client: it already handles
auth, pagination and API versioning, and it honours `GH_TOKEN`, which is how the
work account is kept out of this (see identity.py).

Secondary rate limits are the interesting failure mode. They are partly per-IP,
and the user contributes manually from a second account on this same machine, so
retrying blind can make things worse for both. `Retry-After` is honoured and
backoff is exponential.
"""

from __future__ import annotations

import json
import random
import subprocess
import time
from typing import Any

from .identity import pipeline_env

MAX_ATTEMPTS = 5
SECONDARY_HINTS = ("secondary rate limit", "abuse detection", "exceeded a secondary")


class GhError(RuntimeError):
    pass


class RateLimited(GhError):
    pass


def _sleep_for(attempt: int, stderr: str) -> float:
    for line in stderr.splitlines():
        if line.lower().startswith("retry-after:"):
            try:
                return float(line.split(":", 1)[1].strip())
            except ValueError:
                pass
    # Full jitter: several stages can be retrying at once.
    return min(60.0, 2.0**attempt) * (0.5 + random.random() / 2)


def _run(args: list[str], stdin: str | None = None) -> str:
    env = pipeline_env()
    last = ""
    for attempt in range(MAX_ATTEMPTS):
        proc = subprocess.run(
            ["gh", *args], input=stdin, capture_output=True, text=True, env=env
        )
        if proc.returncode == 0:
            return proc.stdout
        last = (proc.stderr or "").strip()
        low = last.lower()

        if any(h in low for h in SECONDARY_HINTS) or "429" in low:
            delay = _sleep_for(attempt, last)
            print(f"    secondary rate limit; backing off {delay:.0f}s")
            time.sleep(delay)
            continue
        if "rate limit" in low and "exceeded" in low:
            raise RateLimited(last)
        # A dropped connection is transient. Treating it as fatal took down an
        # entire unattended daily run -- discovery and ledger included -- for a
        # blip that had cleared minutes later.
        transient = (" 500", " 502", " 503", " 504", "timeout",
                     "error connecting", "connection reset", "could not resolve",
                     "network is unreachable", "eof occurred", "tls handshake")
        if any(c in low for c in transient):
            time.sleep(_sleep_for(attempt, last))
            continue
        raise GhError(f"gh {' '.join(args[:3])}: {last}")

    raise RateLimited(f"gh {' '.join(args[:3])} gave up after {MAX_ATTEMPTS}: {last}")


def rest(path: str, *, method: str = "GET", paginate: bool = False,
         jq: str | None = None, fields: dict[str, Any] | None = None) -> Any:
    args = ["api", path]
    if method != "GET":
        args += ["--method", method]
    for key, value in (fields or {}).items():
        args += ["-f", f"{key}={value}"]
    if paginate:
        args += ["--paginate"]
    if jq:
        args += ["--jq", jq]
    out = _run(args).strip()
    if not out:
        return None
    if jq:
        return [json.loads(l) for l in out.splitlines()] if "\n" in out else json.loads(out)
    if paginate:
        # --paginate concatenates JSON arrays; merge them.
        merged: list[Any] = []
        dec = json.JSONDecoder()
        idx = 0
        while idx < len(out):
            val, end = dec.raw_decode(out, idx)
            merged.extend(val) if isinstance(val, list) else merged.append(val)
            idx = end
            while idx < len(out) and out[idx] in " \n\r\t":
                idx += 1
        return merged
    return json.loads(out)


def graphql(query: str, **variables: Any) -> dict[str, Any]:
    body = json.dumps({"query": query, "variables": variables})
    out = _run(["api", "graphql", "--input", "-"], stdin=body)
    data = json.loads(out)
    if "errors" in data:
        msgs = "; ".join(e.get("message", str(e)) for e in data["errors"])
        # A missing repo or renamed label should skip that repo, not kill the run.
        raise GhError(f"graphql: {msgs}")
    return data["data"]


def exists(path: str) -> bool:
    try:
        rest(path)
        return True
    except GhError:
        return False


def rate_budget() -> dict[str, int]:
    r = rest("rate_limit")["resources"]
    return {
        "core": r["core"]["remaining"],
        "graphql": r["graphql"]["remaining"],
        "search": r["search"]["remaining"],
    }
