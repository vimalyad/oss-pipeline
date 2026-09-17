"""Fork, clone, harden. The only place clones come into existence.

Every clone is hardened immediately after creation and before anything is
written, because `harden_clone` is what makes the difference between a commit
attributed to `vimalyad` and one attributed to the work account.
"""

from __future__ import annotations

import shutil
import subprocess
import time
from pathlib import Path

from . import audit, ghapi
from .identity import ROOT, IdentityError, assert_clone, harden_clone, load_identity, pipeline_env
from .models import Candidate

WORK = ROOT / "work"


def clone_dir(cand: Candidate) -> Path:
    return WORK / cand.repo.replace("/", "__")


def _git(clone: Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(
        ["git", "-C", str(clone), *args],
        capture_output=True, text=True, env=pipeline_env(),
    )
    if check and proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args[:3])} failed: {proc.stderr.strip()[:300]}")
    return proc.stdout.strip()


def ensure_fork(repo: str, *, execute: bool = False) -> str:
    """Fork `repo` under the OSS account. Returns the fork's full name."""
    ident = load_identity()
    fork = f"{ident.login}/{repo.split('/')[1]}"

    if ghapi.exists(f"repos/{fork}"):
        return fork
    if not execute:
        print(f"    [dry-run] would fork {repo} -> {fork}")
        return fork

    # Must be POST: a GET on this path merely *lists* existing forks.
    ghapi.rest(f"repos/{repo}/forks", method="POST")
    audit.record("fork", detail=f"{repo} -> {fork}")
    for _ in range(30):                # forking is asynchronous
        time.sleep(2)
        if ghapi.exists(f"repos/{fork}"):
            return fork
    raise RuntimeError(f"fork {fork} did not become available")


def prepare(cand: Candidate, *, execute: bool = False, fresh: bool = False) -> Path:
    """Clone (or reuse) the upstream repo with our fork as the push remote."""
    ident = load_identity()
    dest = clone_dir(cand)
    fork = ensure_fork(cand.repo, execute=execute)

    if fresh and dest.exists():
        shutil.rmtree(dest)

    if not dest.exists():
        if not execute:
            print(f"    [dry-run] would clone {cand.repo} -> {dest}")
            return dest
        WORK.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "clone", "--filter=blob:none",
             f"https://github.com/{cand.repo}.git", str(dest)],
            check=True, capture_output=True, text=True, env=pipeline_env(),
        )
        audit.record("clone", slug=cand.slug, detail=str(dest))

    # A clone left broken by an interrupted fetch must be replaced, not used.
    if dest.exists():
        sane, why = clone_is_sane(dest)
        if not sane:
            print(f"    discarding broken clone: {why}")
            audit.record("clone_discarded", slug=cand.slug, detail=why)
            shutil.rmtree(dest)
            if not execute:
                return dest
            subprocess.run(
                ["git", "clone", "--filter=blob:none",
                 f"https://github.com/{cand.repo}.git", str(dest)],
                check=True, capture_output=True, text=True, env=pipeline_env(),
            )
            audit.record("clone", slug=cand.slug, detail=f"re-cloned {dest}")

    if not dest.exists():
        return dest

    # Harden BEFORE any branch or commit exists.
    harden_clone(dest, ident)
    remotes = _git(dest, "remote", check=False).split("\n")
    if "fork" not in remotes:
        _git(dest, "remote", "add", "fork", f"https://github.com/{fork}.git")
    else:
        _git(dest, "remote", "set-url", "fork", f"https://github.com/{fork}.git")

    assert_clone(dest, ident)   # never proceed on an unproven clone
    return dest


# A bug fix touches a handful of files. If the working tree claims to differ
# from HEAD across a large fraction of the repo, the clone is broken, not the
# patch -- pytorch/vision was cloned during a network outage and reported all
# 708 tracked files as modified, so pytest was handed a LICENSE file as a test
# target. Reasoning about a broken clone is worse than refusing to.
SANITY_MAX_FRACTION = 0.30
SANITY_MAX_FILES = 150


def clone_is_sane(clone: Path) -> tuple[bool, str]:
    if not (clone / ".git").exists():
        return False, "no .git directory"
    tracked = _git(clone, "ls-files", check=False).count("\n") + 1
    changed = [l for l in _git(clone, "diff", "--name-only", "HEAD",
                               check=False).split("\n") if l]
    if not tracked:
        return False, "no tracked files"
    if len(changed) > SANITY_MAX_FILES or len(changed) > tracked * SANITY_MAX_FRACTION:
        return False, (f"{len(changed)} of {tracked} tracked files differ from HEAD "
                       f"-- clone is broken, not a patch this large")
    return True, "ok"


def default_branch(clone: Path) -> str:
    head = _git(clone, "symbolic-ref", "refs/remotes/origin/HEAD", check=False)
    return head.rsplit("/", 1)[-1] if head else "main"


def start_branch(cand: Candidate, clone: Path) -> str:
    """Branch from a freshly fetched upstream default branch."""
    base = default_branch(clone)
    _git(clone, "fetch", "origin", base)
    branch = f"fix/issue-{cand.issue}"
    _git(clone, "checkout", "-B", branch, f"origin/{base}")
    cand.branch = branch
    return branch


# Past this age, rebuilding the prior work onto current main is not a takeover,
# it is an archaeology project: pytorch/vision#796 was opened in 2019 and last
# touched in 2021, and merging main into it conflicts and yields a diff no one
# could review. Start fresh from main and credit the original author instead.
TAKEOVER_MAX_AGE_DAYS = 365


def takeover_branch(cand: Candidate, clone: Path) -> str:
    """For a stale-PR takeover: build on the prior author's commits when that is
    still sane, otherwise start clean and credit them in the PR body."""
    sig = cand.pr_signal
    base = default_branch(clone)
    branch = f"fix/issue-{cand.issue}"
    _git(clone, "fetch", "origin", base)

    def fresh(why: str) -> str:
        print(f"    {why}; starting fresh from {base} "
              f"(prior author still credited in the PR body)")
        _git(clone, "merge", "--abort", check=False)
        _git(clone, "checkout", "-f", "-B", branch, f"origin/{base}")
        cand.branch = branch
        return branch

    if not sig:
        return fresh("no prior PR recorded")

    age = sig.days_since_commit or 0
    if age > TAKEOVER_MAX_AGE_DAYS:
        return fresh(f"PR #{sig.number} last moved {age // 365}y ago, too old to rebase")

    try:
        _git(clone, "fetch", "origin", f"pull/{sig.number}/head:{branch}-prior")
        _git(clone, "checkout", "-B", branch, f"{branch}-prior")
    except RuntimeError:
        return fresh(f"could not fetch PR #{sig.number}")

    # A failed merge must not be ignored: it leaves MERGE_HEAD set and the tree
    # conflicted, and the next step would patch on top of that.
    proc = subprocess.run(
        ["git", "-C", str(clone), "merge", "--no-edit", f"origin/{base}"],
        capture_output=True, text=True, env=pipeline_env(),
    )
    if proc.returncode != 0 or (clone / ".git" / "MERGE_HEAD").exists():
        return fresh(f"merging {base} into PR #{sig.number} conflicts")

    print(f"    based on PR #{sig.number} by @{sig.author} (authorship preserved)")
    cand.branch = branch
    return branch


def diff_stat(clone: Path) -> str:
    return _git(clone, "diff", "--stat", "HEAD", check=False)


def changed_files(clone: Path, base: str) -> list[str]:
    """Files THIS branch changed. Fetches first, because a stale origin makes
    the three-dot diff include every upstream commit since the clone."""
    subprocess.run(["git", "-C", str(clone), "fetch", "-q", "origin", base],
                   capture_output=True, env=pipeline_env())
    out = _git(clone, "diff", "--name-only", f"origin/{base}...HEAD", check=False)
    return [l for l in out.split("\n") if l]
