"""Repo-level gates, cached.

These change slowly and cost API calls, so they are fetched once per repo per
week rather than per candidate. AI-policy detection is a cheap keyword prefilter
followed by an LLM adjudication only when a keyword actually fires -- the
keywords alone cannot tell "we require disclosure of AI assistance" from
"we do not accept AI-generated contributions", and that distinction decides
whether the repo is usable at all.
"""

from __future__ import annotations

import base64
import json
import re
import subprocess
from datetime import datetime, timedelta, timezone

from . import ghapi, store
from .models import RepoFacts

CACHE_DAYS = 7

POLICY_FILES = [
    "CONTRIBUTING.md", ".github/CONTRIBUTING.md", "docs/CONTRIBUTING.md",
    ".github/PULL_REQUEST_TEMPLATE.md", ".github/pull_request_template.md",
    "CONTRIBUTING.rst", "AGENTS.md", ".github/COPILOT_INSTRUCTIONS.md",
]

AI_KEYWORDS = re.compile(
    r"\b(ai[- ]generated|ai[- ]assisted|generative ai|llm|chatgpt|copilot|claude|"
    r"machine[- ]generated|artificial intelligence|ai tool)\b", re.I,
)
DCO_KEYWORDS = re.compile(r"signed-off-by|developer certificate of origin|\bDCO\b", re.I)
CLA_KEYWORDS = re.compile(r"contributor licen[cs]e agreement|\bCLA\b(?!SS)", re.I)

TEST_PATHS = (
    "tests", "test", "spec", "__tests__", "t",
    "pytest.ini", "tox.ini", "noxfile.py", "conftest.py",
    "jest.config.js", "vitest.config.ts", "vitest.config.js",
)

ELIGIBILITY = """A repository's contributor docs are on stdin. Extract the rules \
that decide whether an outside pull request is ELIGIBLE AT ALL. Output ONLY JSON:

{"required_issue_labels": [...], "forbidden_issue_labels": [...], "quote": "..."}

- "required_issue_labels": labels a linked issue MUST carry for a PR to be
  accepted. Example: "We accept external pull requests only for issues labelled
  `help wanted`" -> ["help wanted"].
- "forbidden_issue_labels": labels that make an issue off limits. Example:
  "Do not open pull requests for any issue marked `core`" -> ["core"].
- "quote": the sentence you took this from, verbatim, or "".
- Both lists empty if the docs state no such rule. Do NOT infer a rule from a
  project merely using a label; it must be stated as a requirement.
Label names exactly as written, without backticks."""

AI_ADJUDICATE = """A repository's contributor docs are on stdin. Decide its \
policy on AI-assisted contributions. Output ONLY JSON:

{"bans": true|false, "requires_disclosure": true|false, "quote": "verbatim sentence or ''"}

- "bans": the project refuses AI-generated or AI-assisted contributions.
- "requires_disclosure": AI assistance is allowed but must be declared.
- Both false if the text only mentions AI incidentally (e.g. the project IS an
  AI tool, or it bans AI-generated *issue reports* but not PRs).
- "quote": the exact sentence you based this on, or "" if neither applies.
Be conservative: only set a flag on an explicit statement about contributions."""


def _read_file(repo: str, path: str) -> str | None:
    try:
        data = ghapi.rest(f"repos/{repo}/contents/{path}")
    except ghapi.GhError:
        return None
    if not isinstance(data, dict) or data.get("encoding") != "base64":
        return None
    try:
        return base64.b64decode(data["content"]).decode("utf-8", "replace")
    except Exception:
        return None


def _adjudicate_ai_policy(text: str) -> tuple[bool, bool, str]:
    """LLM call, only reached when a keyword already matched."""
    excerpts = []
    for m in AI_KEYWORDS.finditer(text):
        excerpts.append(text[max(0, m.start() - 600): m.end() + 600])
        if len(excerpts) >= 6:
            break
    payload = "\n\n---\n\n".join(excerpts)

    proc = subprocess.run(
        ["claude", "-p", AI_ADJUDICATE, "--model", "sonnet",
         "--disallowed-tools", "Read", "Write", "Edit", "Bash", "WebFetch", "WebSearch"],
        input=payload, capture_output=True, text=True, timeout=180,
    )
    if proc.returncode != 0:
        # Unknown policy is not permission. Fail closed: require disclosure.
        return False, True, "(policy adjudication failed; assuming disclosure required)"
    try:
        start = proc.stdout.find("{")
        raw = json.loads(proc.stdout[start: proc.stdout.rfind("}") + 1])
    except (ValueError, json.JSONDecodeError):
        return False, True, "(policy adjudication unparseable; assuming disclosure required)"
    return bool(raw.get("bans")), bool(raw.get("requires_disclosure")), str(raw.get("quote") or "")


def _eligibility_rules(text: str) -> tuple[list[str], list[str], str]:
    """Which issues a project will accept an outside PR for at all.

    cli/cli states "We accept external pull requests only for issues labelled
    `help wanted`". We read that file already but only mined it for AI/DCO/CLA,
    so a PR went out against a `bug`-labelled issue and was closed by a bot rule
    the project documents plainly.
    """
    proc = subprocess.run(
        ["claude", "-p", ELIGIBILITY, "--model", "sonnet",
         "--disallowed-tools", "Read", "Write", "Edit", "Bash", "WebFetch", "WebSearch"],
        input=text[:40_000], capture_output=True, text=True, timeout=180,
    )
    if proc.returncode != 0:
        return [], [], ""
    try:
        start = proc.stdout.find("{")
        raw = json.loads(proc.stdout[start: proc.stdout.rfind("}") + 1])
    except (ValueError, json.JSONDecodeError):
        return [], [], ""
    norm = lambda xs: [str(x).strip().strip("`").lower() for x in (xs or []) if str(x).strip()]
    return norm(raw.get("required_issue_labels")), norm(raw.get("forbidden_issue_labels")), \
        str(raw.get("quote") or "")


def _has_tests(repo: str) -> bool:
    try:
        tree = ghapi.rest(f"repos/{repo}/contents/")
    except ghapi.GhError:
        return False
    names = {e["name"].lower() for e in tree} if isinstance(tree, list) else set()
    if names & set(TEST_PATHS):
        return True
    # A CI workflow that runs a test command also counts.
    try:
        wf = ghapi.rest(f"repos/{repo}/contents/.github/workflows")
    except ghapi.GhError:
        return False
    return bool(isinstance(wf, list) and wf)


def _merged_first_time_pr(repo: str, days: int) -> bool:
    since = (datetime.now(timezone.utc) - timedelta(days=days)).date().isoformat()
    q = f"repo:{repo} is:pr is:merged merged:>={since}"
    query = """
    query($q:String!) {
      search(query:$q, type:ISSUE, first:40) {
        nodes { ... on PullRequest { authorAssociation } }
      }
    }
    """
    try:
        nodes = ghapi.graphql(query, q=q)["search"]["nodes"]
    except ghapi.GhError:
        return False
    return any(
        (n or {}).get("authorAssociation") in ("FIRST_TIME_CONTRIBUTOR", "CONTRIBUTOR", "NONE")
        for n in nodes
    )


def facts(repo: str, *, refresh: bool = False, first_time_window: int = 90) -> RepoFacts:
    cached = store.load_repo_facts(repo)
    if cached and not refresh and cached.fetched_at:
        age = datetime.now(timezone.utc) - datetime.fromisoformat(cached.fetched_at)
        if age < timedelta(days=CACHE_DAYS):
            return cached

    meta = ghapi.rest(f"repos/{repo}")
    docs, has_contributing = [], False
    for path in POLICY_FILES:
        body = _read_file(repo, path)
        if body:
            docs.append(f"### {path}\n{body}")
            if "contributing" in path.lower():
                has_contributing = True
    blob = "\n\n".join(docs)

    bans = requires_disclosure = False
    quote = ""
    if blob and AI_KEYWORDS.search(blob):
        bans, requires_disclosure, quote = _adjudicate_ai_policy(blob)

    required, forbidden, elig_quote = _eligibility_rules(blob) if blob else ([], [], "")

    out = RepoFacts(
        repo=repo,
        stars=meta.get("stargazers_count", 0),
        has_contributing=has_contributing,
        bans_ai_prs=bans,
        requires_ai_disclosure=requires_disclosure,
        ai_policy_quote=quote,
        requires_dco=bool(blob and DCO_KEYWORDS.search(blob)),
        requires_cla=bool(blob and CLA_KEYWORDS.search(blob)),
        has_tests=_has_tests(repo),
        primary_language=(meta.get("language") or ""),
        merged_first_time_pr_90d=_merged_first_time_pr(repo, first_time_window),
        required_issue_labels=required,
        forbidden_issue_labels=forbidden,
        eligibility_quote=elig_quote,
        fetched_at=datetime.now(timezone.utc).isoformat(timespec="seconds"),
    )
    store.save_repo_facts(out)
    return out
