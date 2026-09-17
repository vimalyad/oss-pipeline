"""Mirror the status report to a secret gist, readable from any device.

Deliberately NOT a claude.ai artifact: this session authenticates as the user's
work account (their team lead's, under the company org), and publishing personal
open-source tracking there would re-link the work and personal identities that
the rest of this pipeline works to keep apart.

A secret gist is unlisted, not private. Anyone holding the URL can read it, so
nothing sensitive goes in it -- only public PR links and counts that are already
visible on GitHub.
"""

from __future__ import annotations

import json
import subprocess

from . import audit
from .identity import ROOT, pipeline_env
from .report import OUT as STATUS

STATE = ROOT / "state" / "gist.json"
FILENAME = "oss-pipeline-status.md"


class ScopeMissing(RuntimeError):
    """The PAT lacks `gist`. Actionable, not fatal."""


class Unreachable(RuntimeError):
    """Could not ask GitHub anything. Says nothing about the token."""


def _gh(args: list[str], stdin: str | None = None) -> tuple[int, str, str]:
    p = subprocess.run(["gh", *args], input=stdin, capture_output=True,
                       text=True, env=pipeline_env())
    return p.returncode, p.stdout, p.stderr


def has_gist_scope() -> bool:
    """Ask the token what it can do, rather than inferring from status codes.

    GitHub answers `POST /gists` with 404, not 403, when the scope is missing --
    it declines to confirm what exists. Inferring "missing scope" from a 404 is
    guesswork; the scopes header states it.
    """
    rc, out, err = _gh(["api", "-i", "user"])
    if rc != 0:
        # A failed call proves nothing about scopes. Reporting "scope missing"
        # for a dropped connection sent the reader to the wrong GitHub page.
        raise Unreachable(err.strip()[:160] or "gh api user failed")
    for line in out.splitlines():
        if line.lower().startswith("x-oauth-scopes:"):
            scopes = {s.strip() for s in line.split(":", 1)[1].split(",")}
            return "gist" in scopes
    return False


def gist_id() -> str | None:
    if not STATE.exists():
        return None
    try:
        return json.loads(STATE.read_text()).get("id")
    except (json.JSONDecodeError, OSError):
        return None


def publish() -> str:
    """Create the gist on first run, update it thereafter."""
    if not STATUS.exists():
        from . import report
        report.write()
    body = STATUS.read_text()

    if not has_gist_scope():      # Unreachable propagates; it is not a scope problem
        raise ScopeMissing("the PAT does not carry the `gist` scope")

    existing = gist_id()
    payload = json.dumps({
        "description": "OSS contribution pipeline — status",
        "files": {FILENAME: {"content": body}},
    })

    if existing:
        rc, out, err = _gh(["api", "-X", "PATCH", f"gists/{existing}", "--input", "-"], payload)
        if rc == 0:
            audit.record("gist_update", detail=existing)
            return f"updated https://gist.github.com/{existing}"
        if "404" in err:          # deleted by hand; fall through and recreate
            STATE.unlink(missing_ok=True)
        elif "scope" in err.lower() or "403" in err:
            raise ScopeMissing(err.strip()[:200])
        else:
            raise RuntimeError(err.strip()[:200])

    # `public: false` is GitHub's "secret" gist: unlisted, not access-controlled.
    payload = json.dumps({
        "description": "OSS contribution pipeline — status",
        "public": False,
        "files": {FILENAME: {"content": body}},
    })
    rc, out, err = _gh(["api", "-X", "POST", "gists", "--input", "-"], payload)
    if rc != 0:
        # 404 here almost always means the scope was revoked since the check.
        if "404" in err or "scope" in err.lower() or "403" in err:
            raise ScopeMissing(err.strip()[:200])
        raise RuntimeError(err.strip()[:200])

    new_id = json.loads(out)["id"]
    STATE.parent.mkdir(parents=True, exist_ok=True)
    STATE.write_text(json.dumps({"id": new_id}, indent=2))
    audit.record("gist_create", detail=new_id)
    return f"created https://gist.github.com/{new_id}"
