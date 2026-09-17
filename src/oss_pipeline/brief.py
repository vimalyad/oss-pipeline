"""Distil an issue thread into the patch spec.

This is the highest-risk stage in the pipeline. A misread thread produces a
confidently wrong PR, so the prompt is written to make *absence* the easy
answer: every field may be empty, nothing may be inferred, and quotes must be
verbatim. An empty `maintainer_desired_approach` correctly fails the scorer
rather than being papered over with a plausible guess.
"""

from __future__ import annotations

import json
import re
import subprocess

from .models import Brief

MODEL = "sonnet"

# Bots review and comment like maintainers but carry no authority. Treating a
# bot's suggestion as the spec is a real failure mode -- we saw a Codex bot
# review on a live thread during development.
BOT_MARKERS = (
    "[bot]", "coderabbitai", "github-actions", "dependabot", "codex",
    "sonarcloud", "codecov", "sweep-ai", "renovate", "greptile", "cursor",
)

SYSTEM = """You extract structured facts from GitHub issue threads. You never \
infer, never summarise charitably, and never fill a field to seem helpful. \
An empty field is a correct and expected answer."""

PROMPT = """Read the GitHub issue thread on stdin and extract ONLY what it \
actually says. Output a single JSON object and nothing else -- no prose, no \
code fence.

Schema (every field optional; use "" or [] when the thread does not say):
{
  "maintainer_desired_approach": "verbatim quote of the approach a MAINTAINER endorsed",
  "approach_source_url": "the comment URL that quote came from",
  "approach_author_association": "OWNER | MEMBER | COLLABORATOR",
  "rejected_approaches": ["verbatim quotes of approaches explicitly ruled out"],
  "acceptance_criteria": ["what 'fixed' means here, in the thread's own words"],
  "open_questions": ["genuine unresolved design questions"],
  "claimed_by": "login of someone who said they are working on it",
  "claimed_at": "ISO date of that claim",
  "reproduction": "repro steps / versions if given"
}

Rules that decide the outcome:

1. AUTHORITY. Only OWNER, MEMBER or COLLABORATOR statements may fill
   `maintainer_desired_approach`. A CONTRIBUTOR or NONE comment is an opinion,
   however confident or detailed. If no maintainer stated or endorsed an
   approach, leave it "" -- do not promote the best-sounding comment.

2. BOTS ARE NOT MAINTAINERS. Ignore logins containing: __BOTS__. Their reviews
   and suggestions carry no authority regardless of association.

3. `rejected_approaches` is for explicit refusals -- "we don't want a new
   dependency", "that would break X", "not going to do it that way". These are
   hard prohibitions and matter more than the desired approach.

   Write each one as a self-contained prohibition, because it will later be
   checked against a diff with no access to this thread. A bare "Unfortunately
   not; mainly because np.histogram accepts variable bin intervals" reads as a
   refusal of the whole feature once the question it answered is gone. Write
   "do not use np.histogram; torch.histc only handles equal bin intervals"
   instead. Keep the maintainer's wording, but make the subject explicit.

4. `open_questions` means the IMPLEMENTATION IS UNDECIDED. Only include a
   question whose answer would change what the patch does. Specifically:
     - a maintainer asked something and nobody answered, OR
     - two maintainers propose incompatible approaches, OR
     - the fix depends on a design call nobody has made.
   These are NOT open questions:
     - a contributor asking for guidance or "what should I do here?" -- if a
       maintainer already stated an approach, the thread HAS converged and a
       newcomer's confusion does not un-converge it
     - a question already answered later in the thread
     - a request for a repro, logs or version info from the reporter
     - rhetorical questions
   A populated `open_questions` disqualifies the issue, so a false positive
   here silently discards good work. Be strict about what qualifies.

5. Quote verbatim. Trim for length with "..." but never paraphrase.
"""


def _is_bot(login: str) -> bool:
    low = (login or "").lower()
    return any(m in low for m in BOT_MARKERS)


def _extract_json(text: str) -> dict:
    """Tolerate a stray fence or preamble; fail loudly on anything else."""
    text = text.strip()
    fence = re.search(r"```(?:json)?\s*(\{.*?\})\s*```", text, re.S)
    if fence:
        text = fence.group(1)
    start = text.find("{")
    if start == -1:
        raise ValueError(f"no JSON object in model output: {text[:200]!r}")
    depth, end = 0, None
    for i, ch in enumerate(text[start:], start):
        depth += (ch == "{") - (ch == "}")
        if depth == 0:
            end = i + 1
            break
    if end is None:
        raise ValueError("unterminated JSON object in model output")
    return json.loads(text[start:end])


def extract(transcript: str, *, model: str = MODEL, timeout: int = 300) -> Brief:
    prompt = PROMPT.replace("__BOTS__", ", ".join(BOT_MARKERS))
    proc = subprocess.run(
        ["claude", "-p", prompt,
         "--model", model,
         "--append-system-prompt", SYSTEM,
         "--disallowed-tools", "Read", "Write", "Edit", "Bash", "WebFetch", "WebSearch"],
        input=transcript, capture_output=True, text=True, timeout=timeout,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"claude -p failed: {proc.stderr.strip()[:300]}")

    raw = _extract_json(proc.stdout)

    # Drop a maintainer approach attributed to a bot or a non-maintainer: the
    # model is instructed not to do this, but the bar is cheap to enforce here.
    assoc = (raw.get("approach_author_association") or "").upper()
    if assoc not in ("OWNER", "MEMBER", "COLLABORATOR"):
        raw["maintainer_desired_approach"] = ""
        raw["approach_source_url"] = ""
        raw["approach_author_association"] = ""

    def as_list(v) -> list[str]:
        if isinstance(v, list):
            return [str(x) for x in v if str(x).strip()]
        return [str(v)] if str(v or "").strip() else []

    return Brief(
        maintainer_desired_approach=str(raw.get("maintainer_desired_approach") or ""),
        approach_source_url=str(raw.get("approach_source_url") or ""),
        approach_author_association=str(raw.get("approach_author_association") or ""),
        rejected_approaches=as_list(raw.get("rejected_approaches")),
        acceptance_criteria=as_list(raw.get("acceptance_criteria")),
        open_questions=as_list(raw.get("open_questions")),
        claimed_by="" if _is_bot(str(raw.get("claimed_by") or ""))
                   else str(raw.get("claimed_by") or ""),
        claimed_at=str(raw.get("claimed_at") or ""),
        reproduction=str(raw.get("reproduction") or ""),
    )
