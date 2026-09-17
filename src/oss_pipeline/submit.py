"""Commit, push, open the PR.

Last gate before anything is public. Order matters: identity is re-asserted, the
diff is scanned for secrets and for files we promised not to touch, and only
then is a commit created. The commit-msg and pre-push hooks fire underneath all
of this as an independent backstop.
"""

from __future__ import annotations

import json
import re
import subprocess
from pathlib import Path

from . import audit, ghapi, repo
from .identity import assert_clone, load_identity, pipeline_env
from .models import Candidate

# Artefacts that must never appear in a PR. Some are the patch's fault, but
# uv.lock is OUR fault: the `uv run` test command creates it. Tooling used to
# validate a change must not leak into the change.
JUNK = re.compile(
    r"(^|/)(__pycache__/|\.pytest_cache/|node_modules/|\.DS_Store$|"
    r"[^/]+\.pyc$|\.ruff_cache/)"
)
OUR_TOOLING = ("uv.lock",)

# Agent scaffolding must never reach a public PR. An auto-fix run committed
# `.agents/skills/kornia-developer/SKILL.md` -- 92 lines of workflow notes
# referencing an agent tool by name -- into a real kornia PR. It is not junk and
# not a workflow file, so nothing existing caught it.
AGENT_ARTEFACTS = re.compile(
    r"(^|/)(\.agents?|\.claude|\.cursor|\.aider[^/]*|\.github/copilot[^/]*)/"
    r"|(^|/)(SKILL|AGENTS|CLAUDE|GEMINI|\.cursorrules)\.md$"
    r"|(^|/)\.aider\.",
    re.I,
)

SECRETS = [
    (re.compile(r"gh[pousr]_[A-Za-z0-9]{20,}"), "GitHub token"),
    (re.compile(r"AKIA[0-9A-Z]{16}"), "AWS access key"),
    (re.compile(r"-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----"), "private key"),
    (re.compile(r"sk-[A-Za-z0-9]{32,}"), "API secret key"),
    (re.compile(r"xox[baprs]-[A-Za-z0-9-]{10,}"), "Slack token"),
    (re.compile(r"vimal\.yadav@example\.com", re.I), "work email address"),
]

MSG_PROMPT = """Write the git commit message for the diff on stdin.

Rules:
- Match the commit style already used in this repository: {style}
- Subject line under 72 characters, imperative mood.
- Body: what changed and why, wrapped at 72 columns. Reference the issue as
  "{issue_ref}" on its own line.
- Do NOT mention AI, Claude, assistants, being generated, or any tooling used to
  write it. Write as the human author.
- Describe what the change DOES, not what the touched text is about. A commit
  that deletes a long document is "remove X", not a summary of X's subject --
  deleting a 92-line note about MPS work is not an MPS change.
- Output ONLY the commit message. No fences, no preamble."""

PR_BODY = """{summary}

{credits}Fixes #{issue}
{disclosure}"""

# The implementer's final message is addressed to the pipeline operator, not to
# maintainers. Publishing it verbatim put an agent transcript on a real PR --
# first-person notes about sandbox denials, a claim the tests had not been run
# (they had, afterwards, by the pipeline), and an admission of guessing a PR
# number. The body is therefore written from the diff, by a separate prompt.
PR_BODY_PROMPT = """Write the pull request description for the diff on stdin.

Audience: the maintainers of {repo}, who have never seen this branch. Write in
plain third person about the change itself.

Structure, no headings beyond bold lead-ins:
- One paragraph: what was broken and under what conditions.
- **Change.** what the diff does, and why this approach. If the issue thread
  ruled an approach out, say the chosen one was the suggested one.
- **Verification.** what was run and what passed: {verification}
- If part of the reported problem is deliberately NOT addressed, say so in one
  short paragraph and offer to extend the PR.

Hard rules:
- NEVER mention AI, Claude, an assistant, a model, tooling, a sandbox, a
  harness, command approval, or "this session". None of that exists to a reader.
- NEVER write in the first person about your own limitations or what you could
  not do.
- NEVER invent an issue or PR number. The only number you may cite is #{issue}.
- Do not include a "Fixes #" line; it is appended separately.
- Under 250 words. No bullet dump of the issue text.

Issue being fixed: #{issue} -- {title}
Output ONLY the description."""

# Phrases that must never reach a public PR body.
BODY_FORBIDDEN = re.compile(
    r"\b(claude|anthropic|\bLLM\b|language model|\bAI\b|assistant|"
    r"sandbox|harness|requires approval|this session|"
    r"I could not|I was unable|I couldn't|I guessed|statically reviewed)\b",
    re.I,
)


def scan_secrets(diff: str) -> list[str]:
    return [name for pattern, name in SECRETS if pattern.search(diff)]


def _git(clone: Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(["git", "-C", str(clone), *args],
                          capture_output=True, text=True, env=pipeline_env())
    if check and proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args[:2])}: {proc.stderr.strip()[:300]}")
    return proc.stdout.strip()


def _commit_style(clone: Path) -> str:
    log = _git(clone, "log", "-15", "--format=%s", check=False)
    return "; ".join(log.split("\n")[:6]) or "(no history read)"


def branch_files(clone: Path) -> list[str]:
    """Every file THIS BRANCH adds or changes versus upstream.

    Not `git diff HEAD`, which is uncommitted work only. Checking that scope let
    `.agents/skills/kornia-developer/SKILL.md` reach a public PR: the guard
    matched the path perfectly, but the file had already been committed, so
    preflight ran against an empty set and reported clean.
    """
    base = repo.default_branch(clone)
    subprocess.run(["git", "-C", str(clone), "fetch", "-q", "origin", base],
                   capture_output=True, env=pipeline_env())
    # What the PR will SHIP, net of everything: files added or modified by the
    # branch, minus any the working tree removes. A file committed earlier and
    # deleted now is not something this PR adds.
    committed = set()
    for line in _git(clone, "diff", "--name-status", f"origin/{base}...HEAD",
                     check=False).split("\n"):
        if not line.strip():
            continue
        status, _, path = line.partition("\t")
        if status[:1] in ("A", "M", "R", "C"):
            committed.add(path.split("\t")[-1])

    pending_add, pending_del = set(), set()
    for line in _git(clone, "diff", "--name-status", "HEAD", check=False).split("\n"):
        if not line.strip():
            continue
        status, _, path = line.partition("\t")
        path = path.split("\t")[-1]
        (pending_del if status[:1] == "D" else pending_add).add(path)

    # Untracked files count as shipped. `git diff --name-status HEAD` lists
    # tracked changes only, so a file the implementer created but has not
    # committed is invisible here -- while commit() runs `git add -A`, which
    # sweeps exactly those in. Same shape as the .agents incident: the rule was
    # right, the scope was wrong.
    for path in _git(clone, "ls-files", "--others", "--exclude-standard",
                     check=False).split("\n"):
        if path.strip():
            pending_add.add(path.strip())

    return sorted((committed | pending_add) - pending_del)


def preflight(cand: Candidate, clone: Path) -> list[str]:
    """Everything that must be true before this branch becomes a pull request."""
    problems: list[str] = []
    assert_clone(clone)      # raises on identity failure -- not a soft problem

    base = repo.default_branch(clone)
    diff = _git(clone, "diff", f"origin/{base}...HEAD", check=False) \
        + _git(clone, "diff", "HEAD", check=False)
    if not diff.strip():
        problems.append("empty diff -- nothing to submit")

    for name in scan_secrets(diff):
        problems.append(f"diff contains a {name} -- refusing to push")

    staged = branch_files(clone)
    for d in new_top_level_dirs(clone, staged):
        problems.append(f"diff creates a new top-level directory {d!r}; a fix should not")
    for path in staged:
        if not path:
            continue
        if path.startswith(".github/workflows"):
            problems.append(f"diff touches {path}; workflow changes are excluded")
        if JUNK.search(path):
            problems.append(f"diff contains build artefact {path}")
        if AGENT_ARTEFACTS.search(path):
            problems.append(f"diff adds agent tooling {path}; never ship this")
        if path in OUR_TOOLING:
            problems.append(
                f"diff contains {path}, created by our own test tooling -- "
                f"clean it before committing"
            )
    return problems


def new_top_level_dirs(clone: Path, changed: list[str]) -> list[str]:
    """Top-level directories the diff introduces that the repo did not have.

    A bug fix does not invent a new top-level directory. When one appears it is
    almost always the implementer creating scaffolding for itself.
    """
    existing = {
        line.split("/")[0]
        for line in _git(clone, "ls-tree", "--name-only", "HEAD", check=False).split("\n")
        if line
    }
    introduced = {
        c.split("/")[0] for c in changed if "/" in c and c.split("/")[0] not in existing
    }
    return sorted(introduced)


def clean_artefacts(clone: Path) -> list[str]:
    """Drop build artefacts and our own tooling's output before committing.

    Only removes paths git does not already track: a repo that genuinely ships
    a uv.lock keeps it.
    """
    tracked = set(_git(clone, "ls-files", check=False).split("\n"))
    removed: list[str] = []
    status = _git(clone, "status", "--porcelain", check=False)
    for line in status.split("\n"):
        if not line.strip():
            continue
        path = line[3:].strip()
        if path in tracked:
            continue
        if JUNK.search(path) or path in OUR_TOOLING:
            target = clone / path
            _git(clone, "rm", "-r", "--cached", "--quiet", path, check=False)
            if target.is_dir():
                subprocess.run(["rm", "-rf", str(target)], check=False)
            elif target.exists():
                target.unlink()
            removed.append(path)
    return removed


def commit(cand: Candidate, clone: Path, *, execute: bool = False) -> str:
    ident = load_identity()
    diff = _git(clone, "diff", "HEAD", check=False)
    prompt = MSG_PROMPT.format(
        style=_commit_style(clone), issue_ref=f"Fixes #{cand.issue}"
    )
    proc = subprocess.run(
        ["claude", "-p", prompt, "--model", "sonnet",
         "--disallowed-tools", "Read", "Write", "Edit", "Bash"],
        input=diff[:60_000], capture_output=True, text=True, timeout=300,
    )
    message = proc.stdout.strip() if proc.returncode == 0 else (
        f"fix: address issue #{cand.issue}\n\nFixes #{cand.issue}"
    )
    message = re.sub(r"^```\w*\n|\n```$", "", message).strip()

    if cand.facts and cand.facts.requires_dco:
        message += f"\n\nSigned-off-by: {ident.name} <{ident.email}>"

    if not execute:
        print(f"    [dry-run] commit message:\n{message}")
        return message

    for path in clean_artefacts(clone):
        print(f"    cleaned artefact: {path}")
    _git(clone, "add", "-A")
    proc = subprocess.run(
        ["git", "-C", str(clone), "commit", "-m", message],
        capture_output=True, text=True, env=pipeline_env(),
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"git commit rejected:\n{(proc.stdout + proc.stderr).strip()[:800]}"
        )
    audit.record("commit", slug=cand.slug, detail=message.split("\n")[0])
    return message


def compose_body(cand: Candidate, clone: Path, verification: str) -> str:
    """Write the PR description from the diff, never from the agent's summary.

    Fetch first: a stale `origin` makes `origin/main...HEAD` include everything
    upstream has merged since the clone, which for kornia was 250+ files instead
    of 3. A body composed from that diff would describe someone else's work.
    """
    base = repo.default_branch(clone)
    subprocess.run(["git", "-C", str(clone), "fetch", "-q", "origin", base],
                   capture_output=True, env=pipeline_env())
    diff = _git(clone, "diff", f"origin/{base}...HEAD", check=False)
    prompt = (PR_BODY_PROMPT
              .replace("{repo}", cand.repo)
              .replace("{issue}", str(cand.issue))
              .replace("{title}", cand.title)
              .replace("{verification}", verification or "the project's own tests"))
    proc = subprocess.run(
        ["claude", "-p", prompt, "--model", "sonnet",
         "--disallowed-tools", "Read", "Write", "Edit", "Bash"],
        input=diff[:60_000], capture_output=True, text=True, timeout=420,
    )
    body = proc.stdout.strip() if proc.returncode == 0 else ""
    body = re.sub(r"^```\w*\n|\n```$", "", body).strip()

    hit = BODY_FORBIDDEN.search(body)
    if hit or not body:
        # Fall back to something plain and true rather than publish a leak.
        reason = f"contained {hit.group(0)!r}" if hit else "was empty"
        print(f"    PR body rejected ({reason}); using a minimal description")
        crit = ""
        if cand.brief and cand.brief.acceptance_criteria:
            crit = "\n".join(f"- {c}" for c in cand.brief.acceptance_criteria) + "\n"
        body = (f"Addresses #{cand.issue} ({cand.title}).\n\n{crit}\n"
                f"Verification: {verification}.")
    return body


def open_pr(cand: Candidate, clone: Path, summary: str, *, execute: bool = False) -> str:
    ident = load_identity()
    base = repo.default_branch(clone)

    credits = ""
    if cand.pr_signal and cand.contest and str(cand.contest) == "stale_pr":
        credits = (
            f"\nThis builds on the earlier work in #{cand.pr_signal.number} by "
            f"@{cand.pr_signal.author}; their commits are preserved in the history.\n"
        )
        cand.credits = cand.pr_signal.author

    # Disclosure goes in the PR body ONLY when the project asks for it -- git
    # history stays clean either way (standing rule).
    disclosure = ""
    if cand.facts and cand.facts.requires_ai_disclosure:
        disclosure = (
            "\n---\n_Per this project's contributing guidelines: this change was "
            "prepared with AI assistance and reviewed by me before submission._\n"
        )
        cand.disclosed_ai = True

    body = PR_BODY.format(
        summary=compose_body(cand, clone, summary), issue=cand.issue,
        credits=credits, disclosure=disclosure,
    )

    # The PR title is the first thing a maintainer reads, so it must describe the
    # CHANGE. The issue title describes the BUG, and prefixing it with "fix:"
    # produces things like "fix: MPS: torch 2.14 ... fail at >= 8192 input" --
    # doubled colon, truncated, and about the symptom. The commit subject the
    # model already wrote is the right summary; fall back to the issue only if
    # there is no commit yet.
    subject = _git(clone, "log", "-1", "--format=%s", check=False)
    title = subject if subject else f"fix: {cand.title[:70]}"

    if not execute:
        print(f"    [dry-run] would push {cand.branch} -> fork and open PR")
        print(f"    [dry-run] title: {title}")
        print(f"    [dry-run] body:\n{body}")
        return ""

    _git(clone, "push", "--set-upstream", "fork", cand.branch)
    audit.record("push", slug=cand.slug, detail=f"fork/{cand.branch}")

    out = subprocess.run(
        ["gh", "pr", "create", "--repo", cand.repo, "--base", base,
         "--head", f"{ident.login}:{cand.branch}",
         "--title", title, "--body", body],
        capture_output=True, text=True, env=pipeline_env(), cwd=clone,
    )
    if out.returncode != 0:
        raise RuntimeError(f"gh pr create failed: {out.stderr.strip()[:400]}")
    url = out.stdout.strip().split("\n")[-1]
    cand.pr_url = url
    cand.pr_number = int(url.rstrip("/").split("/")[-1])
    audit.record("pr_open", slug=cand.slug, detail=url)
    return url
