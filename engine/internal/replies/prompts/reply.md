Write a reply to this pull-request review comment, from the PR author.

Context you may use:
- The PR: {{repo}}#{{pr}} -- {{title}}
- Commits pushed since the review: {{commits}}
- The FULL current diff of the PR is below. Check the feedback against it before
  claiming anything is outstanding: if the diff already contains a change, it is
  DONE, whatever the commit subjects suggest.

<diff>
{{diff}}
</diff>

The reviewer said:
"""{{feedback}}"""

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
Output ONLY the reply text.
