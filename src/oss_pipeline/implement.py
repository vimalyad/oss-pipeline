"""Write the patch.

The brief is the spec, not a suggestion: `maintainer_desired_approach` is what
to build, `rejected_approaches` are hard prohibitions, `acceptance_criteria` is
the definition of done. A patch that contradicts a rejected approach is blocked
before submit rather than argued about on the PR.

The standing rule for the diff is *match the host repo*. SOLID is the quality
bar for code we add; it is never a licence to restructure code we merely touch.
Turning up to a mature codebase with a preferred architecture is one of the
fastest ways to get a PR closed.
"""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

from . import audit, repo, toolchain
from .identity import assert_clone, pipeline_env
from .models import Candidate

PATCH_PROMPT = """You are fixing ONE issue in an existing open-source repository \
that you did not write. Work in the current directory.

## The issue
{repo}#{issue}: {title}
{url}

{body}

## The spec, taken from the issue thread
{spec}

## Absolute constraints

1. MATCH THIS REPOSITORY. Read neighbouring code first and copy its idioms,
   naming, error handling, test style and comment density. A diff that reads
   like the surrounding code is the goal. Do NOT restructure, do NOT introduce
   an abstraction the codebase does not already use, do NOT reformat untouched
   lines.
2. MINIMAL. Change the least that fixes the issue. No drive-by refactors, no
   dependency additions unless the thread explicitly calls for one.
3. NEVER contradict a "must NOT" item above. Those are maintainer refusals.
4. Add or extend a test that fails before your change and passes after, in the
   repo's existing test style and location.
5. Do NOT modify anything under .github/ -- workflows especially.
6. Do NOT add any comment, docstring, commit message or file mentioning AI,
   Claude, an assistant, or that this was generated. Write it as ordinary
   human-authored code.
7. Do NOT run `git commit`, `git push`, `git rebase` or `git reset`. Leave every
   change in the working tree. The pipeline commits and pushes, and that is
   where secret scanning and artefact checks run -- committing yourself skips
   them.
8. Do NOT create files outside the change: no new top-level directories, no
   notes-to-self, no workflow or skill documents. Only touch what the fix needs.
9. NEVER invent an issue or PR number. If a changelog or docs entry wants a PR
   number, the pull request does not exist yet -- reference only the issue
   number given above, never a guessed one.
10. If the issue turns out to be unfixable as specified, or the spec is too vague
   to implement safely, make NO changes and say exactly why. A clean refusal is
   a far better outcome than a speculative patch.

## When done
Run the repo's own tests and linters and make them pass:
{commands}

Then print a one-paragraph summary of what you changed and why."""

VERIFY_PROMPT = """A patch was written for this issue. Check it against the \
constraints and output ONLY JSON:

{"violates": ["..."], "touches_workflows": true|false, "has_test": true|false, \
"ai_mentions": ["..."], "summary": "one sentence"}

- "violates": ways the diff does a SPECIFIC thing a maintainer refused.

  Read the refusals carefully. They are verbatim quotes lifted out of a thread,
  so a reply like "Unfortunately not; mainly because np.histogram accepts
  variable bin intervals" is refusing *np.histogram*, not refusing the feature
  the issue asks for. Flag a violation only when the diff actually does the
  named thing. Never infer from a quote that the maintainer rejected the whole
  request -- the issue would not be open and labelled for contribution if they
  had. If a refusal is too vague to check against the diff, ignore it.
- "ai_mentions": any comment/string in the diff mentioning AI, Claude, an
  assistant, or being generated. Must be empty.
- Be strict; an empty patch is not a violation, it is a valid refusal.

Maintainer refusals:
{rejected}

Diff:
{diff}"""


def _spec_block(cand: Candidate) -> str:
    b = cand.brief
    if not b:
        return "(no brief -- refuse to implement)"
    out = []
    if b.maintainer_desired_approach:
        out.append(f'MAINTAINER WANTS ({b.approach_author_association}): '
                   f'"{b.maintainer_desired_approach}"')
        if b.approach_source_url:
            out.append(f"  source: {b.approach_source_url}")
    for r in b.rejected_approaches:
        out.append(f"MUST NOT: {r}")
    for a in b.acceptance_criteria:
        out.append(f"DONE WHEN: {a}")
    if b.reproduction:
        out.append(f"REPRODUCTION: {b.reproduction}")
    return "\n".join(out)


def _run(clone: Path, cmd: str, timeout: int = 1200) -> tuple[int, str]:
    """Run one of the TARGET repo's own commands.

    These are `go test`, `npm test`, `cargo test` -- code the other project
    controls, not ours. It has no business seeing the GitHub token, so strip it
    here. Our own git and gh calls still use pipeline_env() and still get it.
    (Stopgap: v2 runs all of this in a container instead.)
    """
    env = pipeline_env()
    for k in ("GH_TOKEN", "GITHUB_TOKEN"):
        env.pop(k, None)
    proc = subprocess.run(
        cmd, shell=True, cwd=clone, capture_output=True, text=True,
        timeout=timeout, env=env,
    )
    return proc.returncode, (proc.stdout + proc.stderr)[-4000:]


def implement(cand: Candidate, clone: Path, *, model: str = "opus",
              timeout: int = 3600) -> dict:
    assert_clone(clone)
    tc = toolchain.detect(clone)
    commands = "\n".join(f"  {c}" for c in [*tc["test"], *tc["lint"]]) or "  (none detected)"

    body = ""
    raw = clone.parent.parent / "state" / "context" / f"{cand.slug}.raw.json"
    if raw.exists():
        issue = json.loads(raw.read_text())["issue"]
        body = (issue.get("body") or "")[:4000]

    prompt = PATCH_PROMPT.format(
        repo=cand.repo, issue=cand.issue, title=cand.title, url=cand.url,
        body=body, spec=_spec_block(cand), commands=commands,
    )

    proc = subprocess.run(
        ["claude", "-p", prompt, "--model", model,
         "--permission-mode", "acceptEdits",
         "--add-dir", str(clone)],
        cwd=clone, capture_output=True, text=True, timeout=timeout,
        env=pipeline_env(),
    )
    audit.record("implement", slug=cand.slug,
                 detail=f"rc={proc.returncode} toolchain={tc['name']}")

    diff = subprocess.run(
        ["git", "-C", str(clone), "diff", "HEAD"],
        capture_output=True, text=True,
    ).stdout

    result = {
        "ok": proc.returncode == 0,
        "summary": proc.stdout.strip()[-3000:],
        "diff_empty": not diff.strip(),
        "toolchain": tc["name"],
        "test_results": [],
    }

    if result["diff_empty"]:
        result["ok"] = False
        # Keep the HEAD: a refusal leads with its conclusion, and truncating
        # from the end left "Sequence` now instantiates) and `LargeList`." as
        # the entire recorded reason for declining a real candidate.
        result["refusal"] = proc.stdout.strip()[:4000]
        return result

    # Exercise the change, not the whole repo: a large suite fails on problems
    # unrelated to our diff, and CI proves the repo green in its own environment.
    changed = subprocess.run(
        ["git", "-C", str(clone), "diff", "--name-only", "HEAD"],
        capture_output=True, text=True,
    ).stdout.split()
    test_cmds, rationale = toolchain.targeted_tests(clone, changed)
    result["test_scope"] = rationale
    print(f"    tests -> {rationale}")

    for cmd in [*test_cmds, *tc["lint"]]:
        rc, out = _run(clone, cmd)
        entry = {"cmd": cmd, "rc": rc, "tail": out[-1200:]}
        if rc != 0 and toolchain.is_environment_failure(rc, out):
            # Say so rather than blaming the patch: this repo cannot be built
            # here, which is a property of the repo, not of the diff.
            entry["environment_failure"] = True
            result["environment_failure"] = True
            result["ok"] = False
        elif rc != 0:
            result["ok"] = False
        result["test_results"].append(entry)

    result["verify"] = verify(cand, clone, diff)
    if result["verify"].get("violates") or result["verify"].get("ai_mentions") \
            or result["verify"].get("touches_workflows"):
        result["ok"] = False
    return result


def verify(cand: Candidate, clone: Path, diff: str) -> dict:
    """Independent check that the patch honoured the maintainer's refusals."""
    rejected = "\n".join(f"- {r}" for r in (cand.brief.rejected_approaches if cand.brief else [])) \
        or "(none stated)"
    prompt = VERIFY_PROMPT.replace("{rejected}", rejected).replace(
        "{diff}", diff[:40_000]).replace("{issue}", str(cand.issue))
    proc = subprocess.run(
        ["claude", "-p", prompt, "--model", "sonnet",
         "--disallowed-tools", "Read", "Write", "Edit", "Bash"],
        capture_output=True, text=True, timeout=600,
    )
    if proc.returncode != 0:
        return {"violates": ["verification step failed to run"], "summary": ""}
    try:
        s = proc.stdout
        return json.loads(s[s.find("{"): s.rfind("}") + 1])
    except (ValueError, json.JSONDecodeError):
        return {"violates": ["verification output unparseable"], "summary": ""}
