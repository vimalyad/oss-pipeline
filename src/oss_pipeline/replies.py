"""Draft replies to maintainer feedback, for the author to approve and post.

The autonomy split says mechanical fixes go out automatically but anything a
human would say stays human-approved. That only works if a draft actually
exists: queuing "needs a reply" with no text leaves the maintainer waiting and
the author with a blank page. A pushed fix that nobody announces reads, from the
reviewer's side, as no response at all.

Nothing here posts anything. `pipeline reply --post` does, once you have read it.
"""

from __future__ import annotations

import json
import subprocess

from . import audit, ghapi, store
from .identity import pipeline_env
from .models import Candidate

DRAFT = """Write a reply to this pull-request review comment, from the PR author.

Context you may use:
- The PR: {repo}#{pr} -- {title}
- Commits pushed since the review: {commits}
- The FULL current diff of the PR is below. Check the feedback against it before
  claiming anything is outstanding: if the diff already contains a change, it is
  DONE, whatever the commit subjects suggest.

<diff>
{diff}
</diff>

The reviewer said:
\"\"\"{feedback}\"\"\"

Rules:
- Plain, direct, collegial. No flattery, no "great catch!", no apology padding.
- If the diff already addresses a point, say so and name what changed and in
  which commit. Be specific enough that the reviewer can verify quickly.
- If part of the feedback is NOT addressed, say so plainly and say what you
  propose. Never imply something is done when it is not -- and never imply
  something is outstanding when the diff shows it is done. Check, do not guess.
- If the reviewer made a factual claim you verified, you may confirm it briefly
  with the evidence. Do not re-explain their own reasoning back to them.
- Never mention AI, Claude, assistants, tooling, automation or a pipeline.
- Under 120 words. No headings. No sign-off.
Output ONLY the reply text."""


def draft_for(cand: Candidate, item: dict, commits: str, diff: str = "") -> str:
    prompt = (DRAFT
              .replace("{repo}", cand.repo).replace("{pr}", str(cand.pr_number))
              .replace("{title}", cand.title)
              .replace("{commits}", commits or "(nothing pushed since)")
              .replace("{diff}", diff[:50_000] or "(diff unavailable)")
              .replace("{feedback}", (item.get("body") or "")[:3000]))
    proc = subprocess.run(
        ["claude", "-p", prompt, "--model", "sonnet",
         "--disallowed-tools", "Read", "Write", "Edit", "Bash"],
        capture_output=True, text=True, timeout=420, env=pipeline_env(),
    )
    if proc.returncode != 0:
        return ""
    from .submit import BODY_FORBIDDEN
    text = proc.stdout.strip()
    hit = BODY_FORBIDDEN.search(text)
    if hit:
        return f"[draft rejected: mentioned {hit.group(0)!r}]"
    return text


def pending(cand: Candidate) -> list[dict]:
    return [q for q in cand.queued_replies if not q.get("posted")]


def refresh_drafts(cand: Candidate) -> int:
    """Generate a draft for every queued item that lacks one."""
    from . import repo as repomod
    clone = repomod.clone_dir(cand)
    commits, diff = "", ""
    if clone.exists():
        base = repomod.default_branch(clone)
        subprocess.run(["git", "-C", str(clone), "fetch", "-q", "origin", base],
                       capture_output=True, env=pipeline_env())
        commits = subprocess.run(
            ["git", "-C", str(clone), "log", f"origin/{base}..HEAD", "--format=%h %s"],
            capture_output=True, text=True,
        ).stdout.strip()[:1500]
        # The diff is what the reviewer will look at. A draft written from commit
        # subjects alone announced three fixes as "not yet done" when the diff
        # already contained all three.
        diff = subprocess.run(
            ["git", "-C", str(clone), "diff", f"origin/{base}...HEAD"],
            capture_output=True, text=True,
        ).stdout

    n = 0
    for item in pending(cand):
        if item.get("draft"):
            continue
        item["draft"] = draft_for(cand, item, commits, diff)
        n += 1
    store.save(cand)
    return n


def post(cand: Candidate, index: int) -> str:
    items = pending(cand)
    if index >= len(items):
        return f"no pending item {index} ({len(items)} pending)"
    item = items[index]
    text = item.get("draft") or ""
    if not text or text.startswith("[draft rejected"):
        return f"item {index} has no usable draft -- run `pipeline replies --draft` first"

    out = subprocess.run(
        ["gh", "pr", "comment", str(cand.pr_number), "--repo", cand.repo, "--body", text],
        capture_output=True, text=True, env=pipeline_env(),
    )
    if out.returncode != 0:
        return f"failed to post: {out.stderr.strip()[:200]}"
    item["posted"] = True
    store.save(cand)
    audit.record("reply_posted", slug=cand.slug, detail=text[:160])
    return f"posted to {cand.repo}#{cand.pr_number}: {out.stdout.strip()}"
