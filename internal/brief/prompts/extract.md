Read the GitHub issue thread on stdin and extract ONLY what it actually says.
Output a single JSON object and nothing else -- no prose, no code fence.

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

2. BOTS ARE NOT MAINTAINERS. Ignore logins containing: {{bots}}. Their reviews
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

5. Quote verbatim. Trim for length with "..." but never paraphrase. Quotes are
   checked against the thread afterwards, and one that cannot be found there is
   discarded.
