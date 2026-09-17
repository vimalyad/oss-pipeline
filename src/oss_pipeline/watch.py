"""Track an open PR through CI, bots and human review.

Autonomy split (decided up front): mechanical failures are fixed and pushed
without asking; anything needing a human reply or a design change is drafted and
queued. Public comments carry the user's name and reputation, so an LLM does not
get to post them unreviewed.
"""

from __future__ import annotations

import json
import re
import subprocess
from pathlib import Path

from . import audit, ghapi, repo, store
from .identity import pipeline_env
from .models import Candidate, Status, transition

MECHANICAL_CHECKS = re.compile(
    r"lint|format|fmt|style|prettier|eslint|clippy|ruff|black|isort|"
    r"typecheck|tsc|mypy|spell|license|dco|commitlint", re.I
)
BOTS = ("coderabbitai", "[bot]", "github-actions", "codecov", "sonarcloud", "codex")

PR_STATE = """
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      number url state merged isDraft mergeable updatedAt
      commits(last:1) {
        nodes { commit {
          oid
          statusCheckRollup {
            state
            contexts(first:60) {
              nodes {
                __typename
                ... on CheckRun { name conclusion detailsUrl }
                ... on StatusContext { context state targetUrl }
              }
            }
          }
        } }
      }
      reviews(last:20) {
        nodes { id state createdAt body author { login } authorAssociation
                comments(first:40) { nodes { id path line body url } } }
      }
      comments(last:30) {
        nodes { id createdAt body url author { login } authorAssociation }
      }
    }
  }
}
"""

CLASSIFY = """Classify each numbered PR feedback item below. Output ONLY a JSON \
array, one object per item, same order:

[{"id": "<the id given>", "class": "mechanical|needs_reply|needs_design|informational", \
"why": "short reason", "action": "what to change, or the reply to send"}]

- "mechanical": a purely local fix with no judgement -- formatting, lint, a
  typo, an import, a missing null check the reviewer spelled out exactly.
- "needs_reply": a question or an objection that a human should answer.
- "needs_design": the change requested alters the approach or scope.
- "informational": bot summaries, CI passing notices, praise, nothing to do.

Items:
{items}"""


def _classify(items: list[dict], *, attempts: int = 2) -> dict[str, dict]:
    """Triage feedback. Falls back to needs_reply, which is the safe direction.

    The fallback records WHY it fell back: a bare "classifier failed" in a
    queued item is undebuggable days later, and this runs unattended.
    """
    if not items:
        return {}
    rendered = "\n\n".join(
        f"[{i['id']}] from {i['author']} ({i['kind']}):\n{i['body'][:1500]}"
        for i in items
    )
    reason = "not attempted"
    for attempt in range(attempts):
        try:
            proc = subprocess.run(
                ["claude", "-p", CLASSIFY.replace("{items}", rendered), "--model", "sonnet",
                 "--disallowed-tools", "Read", "Write", "Edit", "Bash"],
                capture_output=True, text=True, timeout=600, env=pipeline_env(),
            )
        except subprocess.TimeoutExpired:
            reason = "classifier timed out after 600s"
            continue
        except OSError as exc:
            reason = f"could not run claude: {exc}"
            break
        if proc.returncode != 0:
            reason = (f"claude exited {proc.returncode}: "
                      f"{(proc.stderr or proc.stdout).strip()[:160]}")
            continue
        try:
            out = proc.stdout
            arr = json.loads(out[out.find("["): out.rfind("]") + 1])
            return {str(o["id"]): o for o in arr}
        except (ValueError, json.JSONDecodeError, KeyError) as exc:
            reason = f"unparseable output ({exc}): {proc.stdout.strip()[:120]}"

    print(f"    classification fell back: {reason}")
    return {i["id"]: {"class": "needs_reply", "why": reason} for i in items}


def poll(cand: Candidate) -> dict:
    """Fetch current PR state and split new feedback by required action."""
    owner, name = cand.repo.split("/")
    pr = ghapi.graphql(PR_STATE, owner=owner, name=name, number=cand.pr_number)[
        "repository"]["pullRequest"]

    commit = (pr["commits"]["nodes"] or [{}])[0].get("commit", {})
    rollup = commit.get("statusCheckRollup") or {}
    failing, awaiting = [], []
    for ctx in (rollup.get("contexts", {}) or {}).get("nodes", []) or []:
        if ctx.get("__typename") == "CheckRun":
            conclusion = ctx.get("conclusion")
            if conclusion in ("FAILURE", "TIMED_OUT", "CANCELLED"):
                failing.append({"name": ctx["name"], "url": ctx.get("detailsUrl", "")})
            elif conclusion == "ACTION_REQUIRED":
                # GitHub holds workflow runs from first-time contributors until a
                # maintainer approves them. Not a failure and not our move -- we
                # must not "fix" it or keep re-pushing at it.
                awaiting.append({"name": ctx["name"], "url": ctx.get("detailsUrl", "")})
        elif ctx.get("__typename") == "StatusContext" and ctx.get("state") in (
            "FAILURE", "ERROR"
        ):
            failing.append({"name": ctx["context"], "url": ctx.get("targetUrl", "")})

    fresh: list[dict] = []
    for rv in pr["reviews"]["nodes"]:
        login = (rv.get("author") or {}).get("login", "?")
        if rv["id"] not in cand.watch_seen and (rv.get("body") or "").strip():
            fresh.append({"id": rv["id"], "author": login, "kind": f"review {rv['state']}",
                          "body": rv["body"], "url": ""})
        for rc in rv.get("comments", {}).get("nodes", []):
            if rc["id"] not in cand.watch_seen:
                fresh.append({"id": rc["id"], "author": login,
                              "kind": f"inline {rc['path']}:{rc.get('line')}",
                              "body": rc["body"], "url": rc.get("url", "")})
    for c in pr["comments"]["nodes"]:
        if c["id"] not in cand.watch_seen:
            fresh.append({"id": c["id"], "author": (c.get("author") or {}).get("login", "?"),
                          "kind": "comment", "body": c.get("body") or "",
                          "url": c.get("url", "")})

    verdicts = _classify(fresh)
    buckets: dict[str, list[dict]] = {
        "mechanical": [], "needs_reply": [], "needs_design": [], "informational": []
    }
    for item in fresh:
        v = verdicts.get(item["id"], {"class": "needs_reply", "why": "unclassified"})
        item.update(cls=v.get("class", "needs_reply"), why=v.get("why", ""),
                    action=v.get("action", ""))
        buckets.setdefault(item["cls"], buckets["needs_reply"]).append(item)

    # CI failures are classified by name, not by an LLM: a lint job is
    # mechanical, a failing integration test may not be.
    for chk in failing:
        key = f"check:{commit.get('oid','')}:{chk['name']}"
        if key in cand.watch_seen:
            continue
        bucket = "mechanical" if MECHANICAL_CHECKS.search(chk["name"]) else "needs_design"
        buckets[bucket].append({
            "id": key, "author": "CI", "kind": "check", "cls": bucket,
            "body": f"check '{chk['name']}' failed", "why": "CI", "url": chk["url"],
            "action": f"make the '{chk['name']}' check pass",
        })

    return {
        "state": pr["state"], "merged": pr["merged"], "mergeable": pr.get("mergeable"),
        "failing_checks": failing, "awaiting_approval": awaiting,
        "buckets": buckets, "head": commit.get("oid", ""),
    }


class BranchDiverged(RuntimeError):
    """Local commits the fork does not have. Never resolved automatically."""


def sync_with_remote(cand: Candidate, clone: Path) -> str:
    """Bring the local branch up to whatever is actually on the PR.

    Maintainers push to contributor branches -- kornia's did, merging main and
    migrating our changelog entry, which left this clone 35 commits behind.
    Editing from a stale tree produces a push that is either rejected or, if
    anyone ever reaches for --force, silently destroys their work. The remote is
    the source of truth for an open PR; local is a cache.
    """
    env = pipeline_env()
    subprocess.run(["git", "-C", str(clone), "fetch", "-q", "fork"],
                   check=True, capture_output=True, env=env)
    remote = f"fork/{cand.branch}"

    ahead = subprocess.run(
        ["git", "-C", str(clone), "rev-list", "--count", f"{remote}..HEAD"],
        capture_output=True, text=True, env=env).stdout.strip()
    if ahead and ahead != "0":
        raise BranchDiverged(
            f"{clone} has {ahead} commit(s) the fork does not -- refusing to "
            f"reset. Push or drop them by hand first."
        )

    behind = subprocess.run(
        ["git", "-C", str(clone), "rev-list", "--count", f"HEAD..{remote}"],
        capture_output=True, text=True, env=env).stdout.strip() or "0"
    if behind != "0":
        subprocess.run(["git", "-C", str(clone), "reset", "--hard", "-q", remote],
                       check=True, capture_output=True, env=env)
    return f"synced ({behind} commit(s) pulled)" if behind != "0" else "already current"


def apply_mechanical(cand: Candidate, clone: Path, items: list[dict],
                     *, execute: bool = False) -> bool:
    """Fix the mechanical items and push. No human in the loop by design."""
    if not items:
        return False
    asks = "\n".join(f"- {i['action'] or i['body'][:300]}" for i in items)
    prompt = (
        f"You are addressing review feedback on your own open pull request in "
        f"this repository (issue #{cand.issue}).\n\n"
        f"Make exactly these changes and nothing else:\n{asks}\n\n"
        "Then run the repo's own tests and linters until they pass.\n\n"
        "Rules:\n"
        "- Leave every change UNCOMMITTED. Do NOT run git commit, git push, "
        "git rebase or git reset. The pipeline commits and pushes, and that "
        "is where secret scanning and artefact checks run.\n"
        "- Do NOT create any file the fix does not need: no new top-level "
        "directories, no notes-to-self, no skill or workflow documents.\n"
        "- Do NOT refactor anything beyond the requested changes.\n"
        "- Do NOT mention AI, Claude, an assistant or tooling in any comment, "
        "message or file."
    )
    if not execute:
        print(f"    [dry-run] would auto-fix {len(items)} mechanical item(s)")
        return False

    try:
        print(f"    {sync_with_remote(cand, clone)}")
    except (BranchDiverged, subprocess.CalledProcessError) as exc:
        print(f"    cannot sync: {exc}")
        return False

    proc = subprocess.run(
        ["claude", "-p", prompt, "--model", "opus",
         "--permission-mode", "acceptEdits", "--add-dir", str(clone)],
        cwd=clone, capture_output=True, text=True, timeout=3600, env=pipeline_env(),
    )
    from . import submit
    problems = submit.preflight(cand, clone)
    if problems:
        print(f"    auto-fix blocked: {problems[0]}")
        return False
    submit.commit(cand, clone, execute=True)
    subprocess.run(["git", "-C", str(clone), "push", "fork", cand.branch],
                   check=True, capture_output=True, env=pipeline_env())
    audit.record("auto_fix", slug=cand.slug, detail=f"{len(items)} items, rc={proc.returncode}")
    return True


def sync(cand: Candidate, *, execute: bool = False) -> str:
    """One watch cycle for one PR."""
    if not cand.pr_number:
        return "no PR"
    state = poll(cand)
    b = state["buckets"]

    if state["merged"]:
        transition(cand, Status.MERGED, note="merged upstream")
        store.save(cand)
        audit.record("merged", slug=cand.slug, detail=cand.pr_url)
        return "MERGED"
    if state["state"] == "CLOSED":
        transition(cand, Status.CLOSED, note="closed without merge")
        store.save(cand)
        return "closed"

    clone = repo.clone_dir(cand)
    fixed = False
    if b["mechanical"] and clone.exists():
        fixed = apply_mechanical(cand, clone, b["mechanical"], execute=execute)

    queued = b["needs_reply"] + b["needs_design"]
    if queued:
        cand.queued_replies.extend(queued)
        if cand.status is Status.PR_OPEN:
            transition(cand, Status.CHANGES_REQUESTED, note=f"{len(queued)} items need a human")

    if execute:
        for item in b["mechanical"] + b["informational"] + queued:
            cand.watch_seen.append(item["id"])
    store.save(cand)

    parts = [f"{k}={len(v)}" for k, v in b.items() if v]
    if state["awaiting_approval"]:
        parts.append(f"CI awaiting maintainer approval ({len(state['awaiting_approval'])})")
    return (("auto-fixed; " if fixed else "") + (", ".join(parts) or "no change"))
